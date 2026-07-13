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
)

type usageAuthorityRuntime struct {
	Service *authorityapp.Service
}

func buildUsageAuthorityRuntime(parent context.Context, cfg *config.Config, log *slog.Logger, opts *BuildOptions, cp *controlPlaneRuntime, policyObs policydecision.Observer) (*usageAuthorityRuntime, []func() error, error) {
	if cfg == nil || !cfg.Accounting.Authority.Enabled {
		return nil, nil, nil
	}
	if parent == nil {
		parent = context.Background()
	}
	testing := opts.Testing
	src, err := configsource.New(cfg.Accounting.Authority)
	if err != nil {
		return nil, nil, err
	}
	evaluationTimeout, err := cfg.Accounting.Authority.EvaluationTimeoutDuration()
	if err != nil {
		return nil, nil, err
	}
	cleanupTimeout, err := cfg.Accounting.Authority.CleanupTimeoutDuration()
	if err != nil {
		return nil, nil, err
	}
	store, closers, err := buildUsageAuthorityStore(parent, cfg, log, testing)
	if err != nil {
		return nil, nil, err
	}
	readiness, err := store.CheckReadiness(parent)
	if err != nil {
		if strings.ToLower(strings.TrimSpace(cfg.Accounting.Authority.StartupPosture)) == "fail_open" {
			store = &authorityRuntimeStore{StateStore: store, fallback: authoritydomain.StatusFromBacking(authoritydomain.BackingCapabilityAdvisoryOnly)}
		} else {
			return nil, closers, fmt.Errorf("runtimebundle: usage authority readiness unavailable")
		}
	} else if readiness.State != authoritydomain.AuthorityStateReady {
		if strings.ToLower(strings.TrimSpace(cfg.Accounting.Authority.StartupPosture)) == "fail_open" {
			store = &authorityRuntimeStore{StateStore: store, fallback: authoritydomain.StatusFromBacking(authoritydomain.BackingCapabilityAdvisoryOnly)}
		} else {
			return nil, closers, fmt.Errorf("runtimebundle: usage authority readiness: state %s", readiness.State)
		}
	}
	failureBehavior := authoritydomain.FailureBehaviorFailClosed
	if strings.EqualFold(strings.TrimSpace(cfg.Accounting.Authority.StartupPosture), "fail_open") {
		failureBehavior = authoritydomain.FailureBehaviorFailOpen
	}
	svc := authorityapp.NewService(src, store, buildAuthorityEvidenceSink(cp, policyObs, opts), clockFromTesting(testing), authorityapp.ServiceOptions{
		EvaluationTimeout:      evaluationTimeout,
		CleanupTimeout:         cleanupTimeout,
		DefaultFailureBehavior: failureBehavior,
	})
	return &usageAuthorityRuntime{Service: svc}, closers, nil
}

// buildAuthorityEvidenceSink constructs the production EvidenceSink adapter that
// projects authority decisions into the policy observer chain and the
// control-plane accounting-authority ledger. It returns nil when there is
// nothing to project to (no control-plane recorder and no operator policy
// observers) so the authority app skips projection entirely and avoids the
// per-decision projection cost when the capability is fully disabled. The
// policy observer path is best-effort; required pre-work accounting evidence
// is enforced by the recorder-aware adapter.
func buildAuthorityEvidenceSink(cp *controlPlaneRuntime, policyObs policydecision.Observer, opts *BuildOptions) authorityapp.EvidenceSink {
	var recorder *controlplane.RecorderService
	if cp != nil {
		recorder = cp.recorder
	}
	hasOperatorObservers := opts != nil && len(opts.Policy.PolicyObservers) > 0
	if recorder == nil && !hasOperatorObservers {
		return nil
	}
	return evidencesink.New(recorder, policyObs)
}

func buildUsageAuthorityStore(parent context.Context, cfg *config.Config, log *slog.Logger, testing TestingOptions) (authorityapp.StateStore, []func() error, error) {
	if override := testing.AuthorityStoreOverride; override != nil {
		return override, nil, nil
	}
	authCfg := cfg.Accounting.Authority
	domainCfg, err := authCfg.DomainConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("runtimebundle: usage authority rules: %w", err)
	}
	at := time.Now().UTC()
	if testing.Clock != nil {
		at = testing.Clock().UTC()
	}
	limitRows, err := authoritystore.LimitRowsFromRules(domainCfg.Rules, at)
	if err != nil {
		return nil, nil, fmt.Errorf("runtimebundle: usage authority limit rows: %w", err)
	}
	ruleWindows := make(map[string]authoritydomain.WindowSpec, len(domainCfg.Rules))
	for _, rule := range domainCfg.Rules {
		ruleWindows[rule.ID] = rule.Window
	}
	readiness := authoritydomain.StatusFromBacking(authoritydomain.BackingCapabilityAtomic)
	if strings.ToLower(strings.TrimSpace(authCfg.StartupPosture)) == "fail_open" {
		readiness = authoritydomain.StatusFromBacking(authoritydomain.BackingCapabilityAdvisoryOnly)
	}
	seed := authoritystore.Config{
		Backing:     authoritydomain.BackingCapabilityAtomic,
		Readiness:   readiness,
		LimitRows:   limitRows,
		RuleWindows: ruleWindows,
	}
	switch strings.ToLower(strings.TrimSpace(authCfg.Store)) {
	case "", "memory":
		return authoritystore.NewMemory(seed), nil, nil
	case "sqlite":
		path := strings.TrimSpace(authCfg.SQLitePath)
		// _txlock=immediate opens write transactions as BEGIN IMMEDIATE so two
		// proxy instances (or two connections) cannot reserve from stale copies
		// of the same SQLite authority database (requirement 11.1).
		dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate"
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			if strings.ToLower(strings.TrimSpace(authCfg.StartupPosture)) == "fail_open" {
				logAuthorityStoreFallback(parent, log, "sqlite", "open", err)
				return authoritystore.NewMemory(seed), nil, nil
			}
			return nil, nil, fmt.Errorf("runtimebundle: usage authority sqlite open")
		}
		// A single connection serializes BEGIN IMMEDIATE writers and avoids
		// "database is locked" contention under the new locking flush.
		sqlDB.SetMaxOpenConns(1)
		if err := sqlDB.PingContext(parent); err != nil {
			_ = sqlDB.Close()
			if strings.ToLower(strings.TrimSpace(authCfg.StartupPosture)) == "fail_open" {
				logAuthorityStoreFallback(parent, log, "sqlite", "ping", err)
				return authoritystore.NewMemory(seed), nil, nil
			}
			return nil, nil, fmt.Errorf("runtimebundle: usage authority sqlite ping")
		}
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			_ = sqlDB.Close()
			if strings.ToLower(strings.TrimSpace(authCfg.StartupPosture)) == "fail_open" {
				logAuthorityStoreFallback(parent, log, "sqlite", "bun", err)
				return authoritystore.NewMemory(seed), nil, nil
			}
			return nil, nil, fmt.Errorf("runtimebundle: usage authority sqlite bun")
		}
		store, err := authoritystore.NewDurable(parent, bunDB, seed)
		if err != nil {
			_ = bunDB.Close()
			if strings.ToLower(strings.TrimSpace(authCfg.StartupPosture)) == "fail_open" {
				logAuthorityStoreFallback(parent, log, "sqlite", "init", err)
				return authoritystore.NewMemory(seed), nil, nil
			}
			return nil, nil, fmt.Errorf("runtimebundle: usage authority durable sqlite")
		}
		return store, []func() error{store.Close}, nil
	case "postgres":
		poolCfg, err := config.ParseDatabasePoolSettings(cfg.Database)
		if err != nil {
			if strings.ToLower(strings.TrimSpace(authCfg.StartupPosture)) == "fail_open" {
				logAuthorityStoreFallback(parent, log, "postgres", "pool_config", err)
				return authoritystore.NewMemory(seed), nil, nil
			}
			return nil, nil, fmt.Errorf("runtimebundle: usage authority postgres pool")
		}
		child, cancel := context.WithTimeout(parent, db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		bunDB, err := db.OpenPostgresBun(child, authCfg.PostgresDSN, db.PoolSettings{
			MaxOpenConns:    poolCfg.MaxOpenConns,
			MaxIdleConns:    poolCfg.MaxIdleConns,
			ConnMaxLifetime: poolCfg.ConnMaxLifetime,
			ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
		})
		if err != nil {
			if strings.ToLower(strings.TrimSpace(authCfg.StartupPosture)) == "fail_open" {
				logAuthorityStoreFallback(parent, log, "postgres", "open", err)
				return authoritystore.NewMemory(seed), nil, nil
			}
			return nil, nil, fmt.Errorf("runtimebundle: usage authority postgres open")
		}
		store, err := authoritystore.NewDurable(parent, bunDB, seed)
		if err != nil {
			_ = bunDB.Close()
			if strings.ToLower(strings.TrimSpace(authCfg.StartupPosture)) == "fail_open" {
				logAuthorityStoreFallback(parent, log, "postgres", "init", err)
				return authoritystore.NewMemory(seed), nil, nil
			}
			return nil, nil, fmt.Errorf("runtimebundle: usage authority durable postgres")
		}
		return store, []func() error{store.Close}, nil
	default:
		return nil, nil, fmt.Errorf("runtimebundle: usage authority store %q is invalid", authCfg.Store)
	}
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
	if err != nil {
		return s.fallback, nil
	}
	if status.State != authoritydomain.AuthorityStateReady {
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

type fixedClock struct {
	now func() time.Time
}

func (c fixedClock) Now() time.Time {
	if c.now != nil {
		return c.now().UTC()
	}
	return time.Now().UTC()
}

func logAuthorityStoreFallback(ctx context.Context, log *slog.Logger, store, phase string, err error) {
	if log == nil {
		return
	}
	log.WarnContext(ctx, "runtimebundle: usage authority store unavailable, falling back to in-memory store",
		slog.String("component", "usage_authority"),
		slog.String("notice", "store_fallback_memory"),
		slog.String("store", store),
		slog.String("phase", phase),
		slog.String("error", err.Error()),
	)
}
