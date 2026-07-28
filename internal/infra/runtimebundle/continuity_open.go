package runtimebundle

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity/bunstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity/sqlitestore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/uptrace/bun"
)

// OpenContinuityStore opens the continuity store described by cfg with a dedicated
// (non-shared) postgres pool; closing the returned store closes that pool.
// Composition roots sharing process-wide pools use [openContinuityStore] instead.
// For Postgres, ctx bounds open + schema migrate (with [db.DefaultPostgresOpenMigrateTimeout]).
// For SQLite, ctx is used for ping and migration DDL. ctx and cfg must be non-nil.
func OpenContinuityStore(ctx context.Context, cfg *config.Config) (b2bua.Store, error) {
	store, _, err := openContinuityStore(ctx, cfg, nil, nil)
	return store, err
}

// openContinuityStore opens the continuity store described by cfg. When pools is
// non-nil the postgres handle is shared through the registry (claimed on success,
// closed once by the registry owner) and the returned closer is a pool no-op;
// when nil the store owns a dedicated handle released by the returned closer.
// A nil closer means there is nothing to close.
func openContinuityStore(ctx context.Context, cfg *config.Config, pools *db.PoolRegistry, migrator *dualPlaneMigrator) (b2bua.Store, func() error, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("continuity: nil config")
	}
	if ctx == nil {
		return nil, nil, fmt.Errorf("continuity: nil context")
	}
	cc := cfg.Continuity
	switch config.EffectiveContinuityStore(cc) {
	case "sqlite":
		path := strings.TrimSpace(cc.SQLitePath)
		if path == "" {
			return nil, nil, fmt.Errorf("continuity: sqlite_path is required when store is \"sqlite\"")
		}
		s, err := sqlitestore.OpenContext(ctx, path)
		if err != nil {
			return nil, nil, err
		}
		return s, s.Close, nil
	case "memory":
		s, err := continuity.NewMemoryStoreFromConfig(cc)
		if err != nil {
			return nil, nil, err
		}
		return s, nil, nil
	case "postgres":
		poolCfg, err := config.ParseDatabasePoolSettings(cfg.Database)
		if err != nil {
			return nil, nil, fmt.Errorf("continuity: %w", err)
		}
		pool := db.PoolSettings{
			MaxOpenConns:    poolCfg.MaxOpenConns,
			MaxIdleConns:    poolCfg.MaxIdleConns,
			ConnMaxLifetime: poolCfg.ConnMaxLifetime,
			ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
		}
		dsn := strings.TrimSpace(cc.PostgresDSN)
		child, cancel := context.WithTimeout(ctx, db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		store, closeFn, err := openPostgresStore(child, dsn, pool, cfg.Database, pools, migrator, postgresStoreLifecycle[b2bua.Store]{
			// Migrate/Verify nil: bunstore.NewContext owns schema preparation.
			Open: func(ctx context.Context, handle *bun.DB) (b2bua.Store, error) {
				s, err := bunstore.NewContext(ctx, handle)
				if err != nil {
					return nil, fmt.Errorf("continuity: prepare postgres schema: %w", err)
				}
				return s, nil
			},
		})
		if err != nil {
			return nil, nil, fmt.Errorf("continuity: open postgres store: %w", err)
		}
		return store, closeFn, nil
	default:
		s := strings.TrimSpace(cc.Store)
		if s == "" {
			s = "(empty)"
		}
		return nil, nil, fmt.Errorf("continuity: store %q is not supported (supported: memory, sqlite, postgres)", s)
	}
}

// NewMemoryContinuityStore creates an in-memory continuity store from the given config section.
func NewMemoryContinuityStore(cfg config.ContinuityConfig) (b2bua.Store, error) {
	cfg.InMemory = true
	return continuity.NewMemoryStoreFromConfig(cfg)
}
