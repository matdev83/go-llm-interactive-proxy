package continuity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity/bunstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity/sqlitestore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
)

// OpenStore is the composition-root factory for b2bua.Store from runtime config.
// It is equivalent to [OpenStoreContext] with [context.Background] as the parent context.
func OpenStore(cfg *config.Config) (b2bua.Store, error) {
	return OpenStoreContext(context.Background(), cfg)
}

// OpenStoreContext opens the continuity store from cfg. For Postgres, ctx is the parent for
// open + schema migrate, bounded by [context.WithTimeout] with [db.DefaultPostgresOpenMigrateTimeout].
// For SQLite, ctx is used for [database/sql.DB.PingContext] and migration DDL.
// ctx must be non-nil.
// For memory stores, ctx is not used but must still be non-nil.
// For sqlite, ctx is honored for open ping and schema migration I/O.
func OpenStoreContext(ctx context.Context, cfg *config.Config) (b2bua.Store, error) {
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
		if !cc.InMemory {
			return nil, fmt.Errorf("continuity: in_memory=false is not valid when store is \"memory\"")
		}
		return newMemoryStoreFromContinuity(cc)
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

func newMemoryStoreFromContinuity(cfg config.ContinuityConfig) (b2bua.Store, error) {
	opts := b2bua.MemoryStoreOptions{}
	if s := strings.TrimSpace(cfg.TTL); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, fmt.Errorf("continuity.ttl: %w", err)
		}
		if d < 0 {
			return nil, fmt.Errorf("continuity.ttl must be non-negative")
		}
		opts.TTL = d
	}
	if cfg.MaxLegs < 0 {
		return nil, fmt.Errorf("continuity: max_legs must be >= 0")
	}
	if cfg.MaxLegs != 0 {
		opts.MaxLegs = cfg.MaxLegs
	}
	return b2bua.NewMemoryStore(opts)
}

// NewMemoryStore is equivalent to OpenStore for the supported in-memory configuration.
// Prefer OpenStore or [OpenStoreContext] at new composition sites.
func NewMemoryStore(cfg config.ContinuityConfig) (b2bua.Store, error) {
	return OpenStoreContext(context.Background(), &config.Config{Continuity: cfg})
}
