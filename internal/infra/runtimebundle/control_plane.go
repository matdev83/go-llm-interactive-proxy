package runtimebundle

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	coreauth "github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/controlplane/ledgerstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/controlplane/observers"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	_ "modernc.org/sqlite" // register "sqlite" driver for durable control-plane stores
)

// controlPlaneRuntime is the assembled control-plane capability for one Build.
// It is nil when the capability is disabled (design "Configuration and
// Readiness Contract"; requirements 5.1, 5.5, 7.1, 7.5, 8.4, 10.5, 10.6).
type controlPlaneRuntime struct {
	enabled      bool
	queryEnabled bool
	store        controlplane.Store
	recorder     *controlplane.RecorderService
	queries      *controlplane.QueryService
	status       *controlplane.Status
	retention    *controlplane.RetentionController
	normalizer   *controlplane.Normalizer
	closer       func() error

	// authFailClosed mirrors auth.event_failure_policy=fail_closed so the auth
	// sink adapter can fail closed on required pre-work recording failures.
	authFailClosed bool
}

// controlPlaneBuildInput groups dependencies for [buildControlPlaneRuntime].
type controlPlaneBuildInput struct {
	StartupContext context.Context
	Cfg            *config.Config
	Log            *slog.Logger
	Clock          func() time.Time
	// StoreOverride, when non-nil, replaces the configured control-plane store.
	// Tests only; production passes nil so the store is built from config.
	StoreOverride controlplane.Store
}

// controlPlaneStoreID is the stable store identifier embedded in assigned
// EventIDs for the standard distribution control-plane ledger. It is fixed per
// process so memory and durable stores in the same binary share an identity
// space (the store assigns the monotonic sequence; the id pairs them).
const controlPlaneStoreID = "lip-std-control-plane"

// buildControlPlaneRuntime constructs the control-plane capability from typed
// config (task 5.1, 5.2; requirements 5.1, 5.4, 5.5, 5.7, 7.1, 7.5, 7.6, 8.4,
// 10.5, 10.6, 10.7).
//
// Behavior:
//   - When cfg.ControlPlane.Enabled is false, returns (nil, nil) so Build does
//     not wrap any source seam and existing behavior is unchanged (design:
//     disabled capability preserves current runtime behavior).
//   - When enabled, builds the configured store (memory, sqlite, or postgres),
//     a normalizer, a shared status, recorder, query service, and optional
//     retention controller. Source seam wrappers fan existing evidence into
//     the recorder without dropping operator-supplied observers.
//   - Startup fail-closed: when recording_policy is required_pre_work or query
//     exposure is enabled, store open/migrate readiness failures fail startup
//     with redacted errors (no DSN/SQL/driver text). Otherwise the capability
//     is marked unavailable and source paths remain best-effort (no wrapping).
func buildControlPlaneRuntime(in controlPlaneBuildInput) (*controlPlaneRuntime, error) {
	if in.Cfg == nil {
		return nil, fmt.Errorf("runtimebundle: control plane: nil config")
	}
	cfg := &in.Cfg.ControlPlane
	if !cfg.Enabled {
		return nil, nil
	}
	startupCtx := in.StartupContext
	if startupCtx == nil {
		startupCtx = context.Background()
	}

	policy := cp.RecordingPolicy(strings.ToLower(strings.TrimSpace(cfg.RecordingPolicy)))
	if policy == "" {
		policy = cp.RecordingBestEffort
	}
	storeName := strings.ToLower(strings.TrimSpace(cfg.Store))
	if storeName == "" {
		storeName = "memory"
	}
	if policy == cp.RecordingRequiredPreWork && storeName == "memory" {
		return nil, fmt.Errorf("runtimebundle: control plane.recording_policy: required_pre_work requires a durable store (sqlite or postgres)")
	}
	requiredCategories := make([]cp.Category, 0, len(cfg.RequiredCategories))
	for _, c := range cfg.RequiredCategories {
		requiredCategories = append(requiredCategories, cp.Category(strings.TrimSpace(c)))
	}

	var clock controlplane.Clock = controlplane.SystemClock{}
	if in.Clock != nil {
		clock = clockFunc{now: in.Clock}
	}

	status := controlplane.NewStatus(cp.CapabilityStatus{
		State:           cp.CapabilityReady,
		RecordingPolicy: policy,
	})

	store, closer, storeErr := buildControlPlaneStore(startupCtx, in.Cfg, in.StoreOverride)
	if storeErr != nil {
		logControlPlaneStoreOpenFailure(startupCtx, in.Log, storeName, storeErr)
		if policy == cp.RecordingRequiredPreWork || cfg.Query.Enabled {
			return nil, redactedStoreUnavailableError()
		}
		status.SetUnavailable(cp.ReasonBackingUnavailable, nowFromClock(clock))
		return &controlPlaneRuntime{
			enabled:        true,
			queryEnabled:   cfg.Query.Enabled,
			status:         status,
			closer:         closer,
			authFailClosed: strings.EqualFold(strings.TrimSpace(in.Cfg.Auth.EventFailurePolicy), "fail_closed"),
		}, nil
	}

	normalizer := controlplane.NewNormalizer(clock, cp.SourceRef{
		Name:    "lip-std",
		Version: "v1",
	}, controlplane.NewScopeFlattener())

	recorder := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy:   policy,
		Required: requiredCategories,
		Clock:    clock,
	})

	maxTimeWindow, err := cfg.Query.MaxTimeWindowDuration()
	if err != nil {
		if closer != nil {
			_ = closer()
		}
		return nil, fmt.Errorf("runtimebundle: control plane: %w", err)
	}

	queries := controlplane.NewQueryService(store, status, controlplane.QueryServiceConfig{
		Enabled:         cfg.Query.Enabled,
		DefaultPageSize: cfg.Query.DefaultPageSize,
		MaxPageSize:     cfg.Query.MaxPageSize,
		MaxTimeWindow:   maxTimeWindow,
	})

	rt := &controlPlaneRuntime{
		enabled:        true,
		queryEnabled:   cfg.Query.Enabled,
		store:          store,
		recorder:       recorder,
		queries:        queries,
		status:         status,
		normalizer:     normalizer,
		closer:         closer,
		authFailClosed: strings.EqualFold(strings.TrimSpace(in.Cfg.Auth.EventFailurePolicy), "fail_closed"),
	}

	if cfg.Retention.Enabled {
		window, err := time.ParseDuration(strings.TrimSpace(cfg.Retention.Window))
		if err != nil {
			if storeErr == nil && closer != nil {
				_ = closer()
			}
			return nil, fmt.Errorf("runtimebundle: control plane retention: %w", err)
		}
		profile := controlplane.RetentionProfileStandard
		if strings.EqualFold(strings.TrimSpace(cfg.RedactionDefault), "strict") {
			profile = controlplane.RetentionProfileStrict
		}
		rt.retention = controlplane.NewRetentionController(store, status, controlplane.RetentionControllerConfig{
			Profile: profile,
			Window:  window,
			Clock:   clock,
		})
	}

	rt.runStartupRetention(startupCtx, in.Log)

	return rt, nil
}

// runStartupRetention applies one best-effort retention maintenance pass when
// retention is configured. It never fails startup: on store failure the
// retention controller degrades capability status with a bounded reason code
// (design "Retention and Redaction Flow"; requirements 6.1, 7.2).
func (r *controlPlaneRuntime) runStartupRetention(ctx context.Context, log *slog.Logger) {
	if r == nil || r.retention == nil {
		return
	}
	if _, err := r.retention.Apply(ctx, time.Time{}, cp.VisibilityDefault); err != nil {
		if log != nil {
			log.WarnContext(
				ctx, "runtimebundle: control plane startup retention maintenance failed; capability degraded",
				slog.String("component", "control_plane"),
				slog.String("notice", "retention_maintenance_failed"),
			)
		}
	}
}

// buildControlPlaneStore opens the configured control-plane event store and
// returns a closer that releases any durable handle. Errors are returned to the
// build caller, which logs bounded diagnostics and then surfaces a redacted
// classified error via [redactedStoreUnavailableError] when startup must fail. When override is
// non-nil it replaces the configured store (tests only); its Close method, if
// implemented, is returned as the closer.
func buildControlPlaneStore(ctx context.Context, cfg *config.Config, override controlplane.Store) (controlplane.Store, func() error, error) {
	if override != nil {
		var closer func() error
		if c, ok := override.(interface{ Close() error }); ok {
			closer = c.Close
		}
		return override, closer, nil
	}
	cpCfg := &cfg.ControlPlane
	store := strings.ToLower(strings.TrimSpace(cpCfg.Store))
	if store == "" {
		store = "memory"
	}
	switch store {
	case "memory":
		mem, err := ledgerstore.NewMemoryStore(ledgerstore.MemoryConfig{
			StoreID: controlPlaneStoreID,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: control plane memory store: %w", err)
		}
		return mem, mem.Close, nil
	case "sqlite":
		path := strings.TrimSpace(cpCfg.SQLitePath)
		if path == "" {
			return nil, nil, fmt.Errorf("runtimebundle: control plane sqlite_path is required")
		}
		dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: control plane open sqlite: %w", err)
		}
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			closeErr := sqlDB.Close()
			return nil, nil, joinErr(fmt.Errorf("runtimebundle: control plane sqlite bun: %w", err), closeErr)
		}
		child, cancel := context.WithTimeout(ctx, db.DefaultSqliteOpenMigrateTimeout)
		defer cancel()
		durable, err := buildDurableControlPlaneStore(child, bunDB)
		if err != nil {
			closeErr := bunDB.Close()
			return nil, nil, joinErr(err, closeErr)
		}
		return durable, durable.Close, nil
	case "postgres":
		dsn := strings.TrimSpace(cpCfg.PostgresDSN)
		if dsn == "" {
			return nil, nil, fmt.Errorf("runtimebundle: control plane postgres_dsn is required")
		}
		poolCfg, err := config.ParseDatabasePoolSettings(cfg.Database)
		if err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: control plane postgres: %w", err)
		}
		pool := db.PoolSettings{
			MaxOpenConns:    poolCfg.MaxOpenConns,
			MaxIdleConns:    poolCfg.MaxIdleConns,
			ConnMaxLifetime: poolCfg.ConnMaxLifetime,
			ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
		}
		child, cancel := context.WithTimeout(ctx, db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		bunDB, err := db.OpenPostgresBun(child, dsn, pool)
		if err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: control plane open postgres: %w", err)
		}
		durable, err := buildDurableControlPlaneStore(child, bunDB)
		if err != nil {
			closeErr := bunDB.Close()
			return nil, nil, joinErr(err, closeErr)
		}
		return durable, durable.Close, nil
	default:
		return nil, nil, fmt.Errorf("runtimebundle: control plane.store %q is not supported (supported: memory, sqlite, postgres)", store)
	}
}

func logControlPlaneStoreOpenFailure(ctx context.Context, log *slog.Logger, storeName string, err error) {
	if log == nil || err == nil {
		return
	}
	log.WarnContext(
		ctx, "runtimebundle: control plane store unavailable",
		slog.String("component", "control_plane"),
		slog.String("notice", "store_unavailable"),
		slog.String("store", storeName),
		slog.String("error", err.Error()),
	)
}

func buildDurableControlPlaneStore(ctx context.Context, bunDB *bun.DB) (*ledgerstore.DurableStore, error) {
	durable, err := ledgerstore.NewDurableStore(ctx, bunDB, ledgerstore.DurableConfig{
		StoreID: controlPlaneStoreID,
	})
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: control plane durable store: %w", err)
	}
	return durable, nil
}

// wrapAuthSink returns an auth event sink that fans out to the delegate and the
// control-plane recorder. When the capability is disabled or the recorder is
// unavailable, the delegate is returned unchanged (requirement 8.4).
func (r *controlPlaneRuntime) wrapAuthSink(delegate coreauth.EventSink) coreauth.EventSink {
	if r == nil || !r.enabled || r.recorder == nil || r.normalizer == nil {
		return delegate
	}
	return observers.NewAuthSinkAdapter(observers.AuthSinkAdapterConfig{
		Delegate:   delegate,
		Normalizer: r.normalizer,
		Recorder:   r.recorder,
		FailClosed: r.authFailClosed,
	})
}

// wrapSecureSession returns a secure-session app.Store decorator that projects
// lifecycle events into the recorder while delegating authoritative behavior
// to the existing store. When disabled, the delegate is returned unchanged.
func (r *controlPlaneRuntime) wrapSecureSession(delegate app.Store) app.Store {
	if r == nil || !r.enabled || r.recorder == nil || r.normalizer == nil {
		return delegate
	}
	return observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate:   delegate,
		Normalizer: r.normalizer,
		Recorder:   r.recorder,
	})
}

// wrapB2BUA returns a B2BUA store decorator that projects attempt lineage into
// the recorder without changing routing or continuity semantics. When disabled,
// the delegate is returned unchanged (requirement 5.1, 5.3, 8.3, 10.7).
func (r *controlPlaneRuntime) wrapB2BUA(delegate b2bua.Store) b2bua.Store {
	if r == nil || !r.enabled || r.recorder == nil || r.normalizer == nil {
		return delegate
	}
	return observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{
		Delegate:   delegate,
		Normalizer: r.normalizer,
		Recorder:   r.recorder,
	})
}

// policyObserver returns the control-plane policy observer adapter, or nil when
// disabled. Composition roots prepend it to operator-supplied observers so
// existing policy decision observer chains keep their current behavior.
func (r *controlPlaneRuntime) policyObserver() policydecision.Observer {
	if r == nil || !r.enabled || r.recorder == nil || r.normalizer == nil {
		return nil
	}
	return observers.NewPolicyObserverAdapter(observers.PolicyObserverAdapterConfig{
		Normalizer: r.normalizer,
		Recorder:   r.recorder,
	})
}

// usageObserver returns the control-plane usage observer adapter, or nil when
// disabled. Composition roots prepend it to operator-supplied observers so
// existing usage observer chains keep their current behavior.
func (r *controlPlaneRuntime) usageObserver() usage.Observer {
	if r == nil || !r.enabled || r.recorder == nil || r.normalizer == nil {
		return nil
	}
	return observers.NewUsageObserverAdapter(observers.UsageObserverAdapterConfig{
		Normalizer: r.normalizer,
		Recorder:   r.recorder,
	})
}

// queriesHandle returns the query service for stdhttp mounting, or nil when the
// capability is disabled, unavailable, or query exposure is off. stdhttp mounts
// the protected query surface only when this is non-nil (task 5.3; requirement
// 2.9, 7.5).
func (r *controlPlaneRuntime) queriesHandle() *controlplane.QueryService {
	if r == nil || !r.enabled || r.queries == nil {
		return nil
	}
	if !r.queriesEnabled() {
		return nil
	}
	return r.queries
}

// statusHandle returns the shared capability status for the protected status
// route and operator diagnostics, or nil when the capability is disabled
// (requirement 7.1).
func (r *controlPlaneRuntime) statusHandle() *controlplane.Status {
	if r == nil || !r.enabled || r.status == nil {
		return nil
	}
	return r.status
}

// retentionHandle returns the retention controller for operator actions, or nil
// when retention is not configured or the backing store is unavailable.
func (r *controlPlaneRuntime) retentionHandle() *controlplane.RetentionController {
	if r == nil || !r.enabled || r.retention == nil {
		return nil
	}
	return r.retention
}

func (r *controlPlaneRuntime) queriesEnabled() bool {
	if r == nil {
		return false
	}
	return r.queryEnabled
}

// redactedStoreUnavailableError returns a stable classified error for
// control-plane store open/migrate failures that must be surfaced to startup
// callers or unprotected HTTP responses (requirement 7.3, 10.5).
//
// It wraps only controlplane.ErrUnavailable so callers can map it via
// errors.Is / controlplane.Classify. The raw underlying infrastructure error
// (DSNs, SQL, driver text) is intentionally NOT preserved in the error chain:
// operator diagnostics are the responsibility of the build caller's logs, not
// the surfaced error.
func redactedStoreUnavailableError() error {
	return fmt.Errorf("runtimebundle: control plane backing store unavailable (see logs for diagnostics): %w", controlplane.ErrUnavailable)
}

// clockFunc adapts a func() time.Time to the controlplane.Clock interface.
type clockFunc struct {
	now func() time.Time
}

func (c clockFunc) Now() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

func nowFromClock(c controlplane.Clock) time.Time {
	if c == nil {
		return time.Now()
	}
	return c.Now()
}

func joinErr(primary, secondary error) error {
	if secondary == nil {
		return primary
	}
	return fmt.Errorf("%w; close error: %v", primary, secondary)
}

// Compile-time assertions that adapter builders satisfy the core-owned ports.
var (
	_ controlplane.Store = (*ledgerstore.MemoryStore)(nil)
	_ controlplane.Store = (*ledgerstore.DurableStore)(nil)
	_ dialect.Name       = dialect.SQLite
)
