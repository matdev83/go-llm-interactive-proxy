package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity/bunstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity/sqlitestore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
)

// OpenContinuityStore opens the continuity store described by cfg.
// For Postgres, ctx bounds open + schema migrate (with [db.DefaultPostgresOpenMigrateTimeout]).
// For SQLite, ctx is used for ping and migration DDL.
// ctx and cfg must be non-nil.
func OpenContinuityStore(ctx context.Context, cfg *config.Config) (b2bua.Store, error) {
	if cfg == nil {
		return nil, fmt.Errorf("continuity: nil config")
	}
	if ctx == nil {
		return nil, fmt.Errorf("continuity: nil context")
	}
	cc := cfg.Continuity
	switch config.EffectiveContinuityStore(cc) {
	case "sqlite":
		path := strings.TrimSpace(cc.SQLitePath)
		if path == "" {
			return nil, fmt.Errorf("continuity: sqlite_path is required when store is \"sqlite\"")
		}
		return sqlitestore.OpenContext(ctx, path)
	case "memory":
		return continuity.NewMemoryStoreFromConfig(cc)
	case "postgres":
		poolCfg, err := config.ParseDatabasePoolSettings(cfg.Database)
		if err != nil {
			return nil, fmt.Errorf("continuity: %w", err)
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
		bunDB, err := db.OpenPostgresBun(child, dsn, pool)
		if err != nil {
			return nil, fmt.Errorf("continuity: open postgres store: %w", err)
		}
		s, err := bunstore.NewContext(child, bunDB)
		if err != nil {
			schemaErr := fmt.Errorf("continuity: prepare postgres schema: %w", err)
			if cerr := bunDB.Close(); cerr != nil {
				return nil, errors.Join(schemaErr, fmt.Errorf("continuity: close db after schema error: %w", cerr))
			}
			return nil, schemaErr
		}
		return s, nil
	default:
		s := strings.TrimSpace(cc.Store)
		if s == "" {
			s = "(empty)"
		}
		return nil, fmt.Errorf("continuity: store %q is not supported (supported: memory, sqlite, postgres)", s)
	}
}

// NewMemoryContinuityStore creates an in-memory continuity store from the given config section.
func NewMemoryContinuityStore(cfg config.ContinuityConfig) (b2bua.Store, error) {
	cfg.InMemory = true
	return continuity.NewMemoryStoreFromConfig(cfg)
}
