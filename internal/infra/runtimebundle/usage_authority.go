package runtimebundle

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/evidencesink"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/uptrace/bun"
)

type usageAuthorityRuntime struct{ Service *authorityapp.Service }

func buildUsageAuthorityRuntime(owner *processResourceOwner, parent context.Context, cfg *config.Config, log *slog.Logger, opts *BuildOptions, cp *controlPlaneRuntime, policyObs policydecision.Observer, registry *db.PoolRegistry, migrator *dualPlaneMigrator) (*usageAuthorityRuntime, error) {
	if cfg == nil || !cfg.Accounting.Authority.Enabled {
		return nil, nil
	}
	if parent == nil {
		parent = context.Background()
	}
	testing := opts.Testing
	src, err := configsource.New(cfg.Accounting.Authority)
	if err != nil {
		return nil, err
	}
	evaluationTimeout, err := cfg.Accounting.Authority.EvaluationTimeoutDuration()
	if err != nil {
		return nil, err
	}
	cleanupTimeout, err := cfg.Accounting.Authority.CleanupTimeoutDuration()
	if err != nil {
		return nil, err
	}
	store, err := buildUsageAuthorityStore(owner, parent, cfg, log, testing, registry, migrator)
	if err != nil {
		return nil, err
	}
	failOpen := strings.EqualFold(strings.TrimSpace(cfg.Accounting.Authority.StartupPosture), "fail_open")
	advisory := authoritydomain.StatusFromBacking(authoritydomain.BackingCapabilityAdvisoryOnly)
	readiness, err := store.CheckReadiness(parent)
	switch {
	case err != nil:
		if !failOpen {
			return nil, fmt.Errorf("runtimebundle: usage authority readiness unavailable")
		}
		store = &authorityRuntimeStore{StateStore: store, fallback: advisory}
	case readiness.State != authoritydomain.AuthorityStateReady:
		if !failOpen {
			return nil, fmt.Errorf("runtimebundle: usage authority readiness: state %s", readiness.State)
		}
		store = &authorityRuntimeStore{StateStore: store, fallback: advisory}
	}
	failureBehavior := authoritydomain.FailureBehaviorFailClosed
	if failOpen {
		failureBehavior = authoritydomain.FailureBehaviorFailOpen
	}
	svc := authorityapp.NewService(src, store, buildAuthorityEvidenceSink(cp, policyObs, opts), clockFromTesting(testing), authorityapp.ServiceOptions{
		EvaluationTimeout: evaluationTimeout, CleanupTimeout: cleanupTimeout, DefaultFailureBehavior: failureBehavior,
	})
	return &usageAuthorityRuntime{Service: svc}, nil
}

func buildAuthorityEvidenceSink(cp *controlPlaneRuntime, policyObs policydecision.Observer, opts *BuildOptions) authorityapp.EvidenceSink {
	if opts != nil && opts.Production.EvidenceSink != nil {
		return opts.Production.EvidenceSink
	}
	var recorder *controlplane.RecorderService
	if cp != nil {
		recorder = cp.recorder
	}
	hasOperatorObservers := opts != nil && (len(opts.Policy.PolicyObservers) > 0 || len(opts.Production.PolicyObservers) > 0)
	if recorder == nil && !hasOperatorObservers {
		return nil
	}
	return evidencesink.New(recorder, policyObs)
}

func buildUsageAuthorityStore(owner *processResourceOwner, parent context.Context, cfg *config.Config, log *slog.Logger, testing TestingOptions, registry *db.PoolRegistry, migrator *dualPlaneMigrator) (authorityapp.StateStore, error) {
	if override := testing.AuthorityStoreOverride; override != nil {
		return override, nil
	}
	authCfg := cfg.Accounting.Authority
	domainCfg, err := authCfg.DomainConfig()
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: usage authority rules: %w", err)
	}
	at := time.Now().UTC()
	if testing.Clock != nil {
		at = testing.Clock().UTC()
	}
	limitRows, err := authoritystore.LimitRowsFromRules(domainCfg.Rules, at)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: usage authority limit rows: %w", err)
	}
	ruleWindows := make(map[string]authoritydomain.WindowSpec, len(domainCfg.Rules))
	for _, rule := range domainCfg.Rules {
		ruleWindows[rule.ID] = rule.Window
	}
	readiness := authoritydomain.StatusFromBacking(authoritydomain.BackingCapabilityAtomic)
	if strings.ToLower(strings.TrimSpace(authCfg.StartupPosture)) == "fail_open" {
		readiness = authoritydomain.StatusFromBacking(authoritydomain.BackingCapabilityAdvisoryOnly)
	}
	seed := authoritystore.Config{Backing: authoritydomain.BackingCapabilityAtomic, Readiness: readiness, LimitRows: limitRows, RuleWindows: ruleWindows}
	posture := authCfg.StartupPosture
	switch strings.ToLower(strings.TrimSpace(authCfg.Store)) {
	case "", "memory":
		return authoritystore.NewMemory(seed), nil
	case "sqlite":
		path := strings.TrimSpace(authCfg.SQLitePath)
		dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate"
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			return authorityStoreUnavailable(parent, log, posture, seed, "sqlite", "open", "runtimebundle: usage authority sqlite open", err)
		}
		sqlDB.SetMaxOpenConns(1)
		if err := sqlDB.PingContext(parent); err != nil {
			_ = sqlDB.Close()
			return authorityStoreUnavailable(parent, log, posture, seed, "sqlite", "ping", "runtimebundle: usage authority sqlite ping", err)
		}
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			_ = sqlDB.Close()
			return authorityStoreUnavailable(parent, log, posture, seed, "sqlite", "bun", "runtimebundle: usage authority sqlite bun", err)
		}
		store, err := authoritystore.NewDurable(parent, bunDB, seed)
		if err != nil {
			_ = bunDB.Close()
			return authorityStoreUnavailable(parent, log, posture, seed, "sqlite", "init", "runtimebundle: usage authority durable sqlite", err)
		}
		owner.Own(store.Close)
		return store, nil
	case "postgres":
		poolCfg, err := config.ParseDatabasePoolSettings(cfg.Database)
		if err != nil {
			return authorityStoreUnavailable(parent, log, posture, seed, "postgres", "pool_config", "runtimebundle: usage authority postgres pool", err)
		}
		child, cancel := context.WithTimeout(parent, db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		store, err := acquireOwnedProcess(child, owner, func(ctx context.Context) (*authoritystore.DurableStore, func() error, error) {
			return openPostgresStore(ctx, authCfg.PostgresDSN, db.PoolSettings{
				MaxOpenConns: poolCfg.MaxOpenConns, MaxIdleConns: poolCfg.MaxIdleConns,
				ConnMaxLifetime: poolCfg.ConnMaxLifetime, ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
			}, cfg.Database, registry, migrator, postgresStoreLifecycle[*authoritystore.DurableStore]{
				Migrate: authoritystore.Migrate, Verify: authoritystore.VerifySchema,
				Open: func(ctx context.Context, handle *bun.DB) (*authoritystore.DurableStore, error) {
					return authoritystore.OpenStore(ctx, handle, seed)
				},
				Close: (*authoritystore.DurableStore).Close,
			})
		})
		if err != nil {
			return authorityStoreUnavailable(parent, log, posture, seed, "postgres", "init", "runtimebundle: usage authority durable postgres", err)
		}
		return store, nil
	default:
		return nil, fmt.Errorf("runtimebundle: usage authority store %q is invalid", authCfg.Store)
	}
}

func authorityStoreUnavailable(ctx context.Context, log *slog.Logger, posture string, seed authoritystore.Config, store, phase, opaque string, err error) (authorityapp.StateStore, error) {
	if strings.ToLower(strings.TrimSpace(posture)) == "fail_open" {
		logAuthorityStoreEvent(ctx, log, slog.LevelWarn, "store_fallback_memory", store, phase, err)
		return authoritystore.NewMemory(seed), nil
	}
	logAuthorityStoreEvent(ctx, log, slog.LevelError, "store_unavailable", store, phase, err)
	return nil, fmt.Errorf("%s", opaque)
}

type authorityRuntimeStore struct {
	authorityapp.StateStore
	fallback authoritydomain.AuthorityStatus
}

func (s *authorityRuntimeStore) CheckReadiness(ctx context.Context) (authoritydomain.AuthorityStatus, error) {
	if s == nil {
		return authoritydomain.StatusFromBacking(authoritydomain.BackingCapabilityDisabled), nil
	}
	if s.StateStore == nil {
		return s.fallback, nil
	}
	status, err := s.StateStore.CheckReadiness(ctx)
	if err != nil || status.State != authoritydomain.AuthorityStateReady {
		return s.fallback, nil
	}
	return status, nil
}

func clockFromTesting(testing TestingOptions) authorityapp.Clock {
	if testing.Clock == nil {
		return nil
	}
	return fixedClock{now: testing.Clock}
}

type fixedClock struct{ now func() time.Time }

func (c fixedClock) Now() time.Time {
	if c.now != nil {
		return c.now().UTC()
	}
	return time.Now().UTC()
}

func logAuthorityStoreEvent(ctx context.Context, log *slog.Logger, level slog.Level, notice, store, phase string, err error) {
	if log == nil {
		return
	}
	msg := "runtimebundle: usage authority store unavailable"
	if notice == "store_fallback_memory" {
		msg = "runtimebundle: usage authority store unavailable, falling back to in-memory store"
	}
	log.LogAttrs(ctx, level, msg,
		slog.String("component", "usage_authority"), slog.String("notice", notice),
		slog.String("store", store), slog.String("phase", phase), slog.String("error", err.Error()))
}
