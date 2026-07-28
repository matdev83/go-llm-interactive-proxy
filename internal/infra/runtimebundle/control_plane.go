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
type controlPlaneRuntime struct {
	enabled, queryEnabled, authFailClosed bool
	store                                 controlplane.Store
	recorder                              *controlplane.RecorderService
	queries                               *controlplane.QueryService
	status                                *controlplane.Status
	retention                             *controlplane.RetentionController
	normalizer                            *controlplane.Normalizer
	closer                                func() error
}

type controlPlaneBuildInput struct {
	StartupContext context.Context
	Cfg            *config.Config
	Log            *slog.Logger
	Clock          func() time.Time
	StoreOverride  controlplane.Store // tests only
	// PostgresPools shares postgres handles via the process registry when non-nil.
	PostgresPools     *db.PoolRegistry
	DualPlaneMigrator *dualPlaneMigrator
}

const controlPlaneStoreID = "lip-std-control-plane"

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
		State: cp.CapabilityReady, RecordingPolicy: policy,
	})

	store, closer, storeErr := buildControlPlaneStore(startupCtx, in.Cfg, in.StoreOverride, in.PostgresPools, in.DualPlaneMigrator)
	if storeErr != nil {
		if in.Log != nil && storeErr != nil {
			in.Log.WarnContext(startupCtx, "runtimebundle: control plane store unavailable",
				slog.String("component", "control_plane"), slog.String("notice", "store_unavailable"),
				slog.String("store", storeName), slog.String("error", storeErr.Error()))
		}
		if policy == cp.RecordingRequiredPreWork || cfg.Query.Enabled {
			return nil, redactedStoreUnavailableError()
		}
		now := time.Now()
		if clock != nil {
			now = clock.Now()
		}
		status.SetUnavailable(cp.ReasonBackingUnavailable, now)
		return &controlPlaneRuntime{
			enabled: true, queryEnabled: cfg.Query.Enabled, status: status, closer: closer,
			authFailClosed: strings.EqualFold(strings.TrimSpace(in.Cfg.Auth.EventFailurePolicy), "fail_closed"),
		}, nil
	}

	normalizer := controlplane.NewNormalizer(clock, cp.SourceRef{Name: "lip-std", Version: "v1"}, controlplane.NewScopeFlattener())
	recorder := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: policy, Required: requiredCategories, Clock: clock,
	})

	maxTimeWindow, err := cfg.Query.MaxTimeWindowDuration()
	if err != nil {
		if closer != nil {
			_ = closer()
		}
		return nil, fmt.Errorf("runtimebundle: control plane: %w", err)
	}

	queries := controlplane.NewQueryService(store, status, controlplane.QueryServiceConfig{
		Enabled: cfg.Query.Enabled, DefaultPageSize: cfg.Query.DefaultPageSize,
		MaxPageSize: cfg.Query.MaxPageSize, MaxTimeWindow: maxTimeWindow,
	})

	rt := &controlPlaneRuntime{
		enabled: true, queryEnabled: cfg.Query.Enabled, store: store, recorder: recorder,
		queries: queries, status: status, normalizer: normalizer, closer: closer,
		authFailClosed: strings.EqualFold(strings.TrimSpace(in.Cfg.Auth.EventFailurePolicy), "fail_closed"),
	}

	if cfg.Retention.Enabled {
		window, err := time.ParseDuration(strings.TrimSpace(cfg.Retention.Window))
		if err != nil {
			if closer != nil {
				_ = closer()
			}
			return nil, fmt.Errorf("runtimebundle: control plane retention: %w", err)
		}
		profile := controlplane.RetentionProfileStandard
		if strings.EqualFold(strings.TrimSpace(cfg.RedactionDefault), "strict") {
			profile = controlplane.RetentionProfileStrict
		}
		rt.retention = controlplane.NewRetentionController(store, status, controlplane.RetentionControllerConfig{
			Profile: profile, Window: window, Clock: clock,
		})
	}

	rt.runStartupRetention(startupCtx, in.Log)
	return rt, nil
}

func (r *controlPlaneRuntime) runStartupRetention(ctx context.Context, log *slog.Logger) {
	if r == nil || r.retention == nil {
		return
	}
	if _, err := r.retention.Apply(ctx, time.Time{}, cp.VisibilityDefault); err != nil && log != nil {
		log.WarnContext(ctx, "runtimebundle: control plane startup retention maintenance failed; capability degraded",
			slog.String("component", "control_plane"), slog.String("notice", "retention_maintenance_failed"))
	}
}

func buildControlPlaneStore(ctx context.Context, cfg *config.Config, override controlplane.Store, pools *db.PoolRegistry, migrator *dualPlaneMigrator) (controlplane.Store, func() error, error) {
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
		mem, err := ledgerstore.NewMemoryStore(ledgerstore.MemoryConfig{StoreID: controlPlaneStoreID})
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
			if closeErr == nil {
				return nil, nil, fmt.Errorf("runtimebundle: control plane sqlite bun: %w", err)
			}
			return nil, nil, fmt.Errorf("runtimebundle: control plane sqlite bun: %w; close error: %v", err, closeErr)
		}
		child, cancel := context.WithTimeout(ctx, db.DefaultSqliteOpenMigrateTimeout)
		defer cancel()
		durable, err := ledgerstore.NewDurableStore(child, bunDB, ledgerstore.DurableConfig{StoreID: controlPlaneStoreID})
		if err != nil {
			closeErr := bunDB.Close()
			if closeErr == nil {
				return nil, nil, fmt.Errorf("runtimebundle: control plane durable store: %w", err)
			}
			return nil, nil, fmt.Errorf("runtimebundle: control plane durable store: %w; close error: %v", err, closeErr)
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
		child, cancel := context.WithTimeout(ctx, db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		durable, closeFn, err := openPostgresStore(child, dsn, db.PoolSettings{
			MaxOpenConns: poolCfg.MaxOpenConns, MaxIdleConns: poolCfg.MaxIdleConns,
			ConnMaxLifetime: poolCfg.ConnMaxLifetime, ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
		}, cfg.Database, pools, migrator, postgresStoreLifecycle[*ledgerstore.DurableStore]{
			// Migrate/Verify nil: NewDurableStore owns schema preparation; Close
			// nil: registry-owned handles are disposed by the registry.
			Open: func(ctx context.Context, handle *bun.DB) (*ledgerstore.DurableStore, error) {
				s, err := ledgerstore.NewDurableStore(ctx, handle, ledgerstore.DurableConfig{StoreID: controlPlaneStoreID})
				if err != nil {
					return nil, fmt.Errorf("runtimebundle: control plane durable store: %w", err)
				}
				return s, nil
			},
		})
		if err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: control plane open postgres: %w", err)
		}
		return durable, closeFn, nil
	default:
		return nil, nil, fmt.Errorf("runtimebundle: control plane.store %q is not supported (supported: memory, sqlite, postgres)", store)
	}
}

func (r *controlPlaneRuntime) wrapAuthSink(delegate coreauth.EventSink) coreauth.EventSink {
	if !r.observerReady() {
		return delegate
	}
	return observers.NewAuthSinkAdapter(observers.AuthSinkAdapterConfig{
		Delegate: delegate, Normalizer: r.normalizer, Recorder: r.recorder, FailClosed: r.authFailClosed,
	})
}

func (r *controlPlaneRuntime) wrapSecureSession(delegate app.Store) app.Store {
	if !r.observerReady() {
		return delegate
	}
	return observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate: delegate, Normalizer: r.normalizer, Recorder: r.recorder,
	})
}

func (r *controlPlaneRuntime) wrapB2BUA(delegate b2bua.Store) b2bua.Store {
	if !r.observerReady() {
		return delegate
	}
	return observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{
		Delegate: delegate, Normalizer: r.normalizer, Recorder: r.recorder,
	})
}

func (r *controlPlaneRuntime) policyObserver() policydecision.Observer {
	if !r.observerReady() {
		return nil
	}
	return observers.NewPolicyObserverAdapter(observers.PolicyObserverAdapterConfig{
		Normalizer: r.normalizer, Recorder: r.recorder,
	})
}

func (r *controlPlaneRuntime) usageObserver() usage.Observer {
	if !r.observerReady() {
		return nil
	}
	return observers.NewUsageObserverAdapter(observers.UsageObserverAdapterConfig{
		Normalizer: r.normalizer, Recorder: r.recorder,
	})
}

func (r *controlPlaneRuntime) observerReady() bool {
	return r != nil && r.enabled && r.recorder != nil && r.normalizer != nil
}

func (r *controlPlaneRuntime) queriesHandle() *controlplane.QueryService {
	if r == nil || !r.enabled || r.queries == nil || !r.queryEnabled {
		return nil
	}
	return r.queries
}

func (r *controlPlaneRuntime) statusHandle() *controlplane.Status {
	if r == nil || !r.enabled || r.status == nil {
		return nil
	}
	return r.status
}

func (r *controlPlaneRuntime) retentionHandle() *controlplane.RetentionController {
	if r == nil || !r.enabled || r.retention == nil {
		return nil
	}
	return r.retention
}

func redactedStoreUnavailableError() error {
	return fmt.Errorf("runtimebundle: control plane backing store unavailable (see logs for diagnostics): %w", controlplane.ErrUnavailable)
}

type clockFunc struct{ now func() time.Time }

func (c clockFunc) Now() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

var (
	_ controlplane.Store = (*ledgerstore.MemoryStore)(nil)
	_ controlplane.Store = (*ledgerstore.DurableStore)(nil)
	_ dialect.Name       = dialect.SQLite
)
