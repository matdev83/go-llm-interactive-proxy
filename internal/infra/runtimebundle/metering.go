package runtimebundle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite" // register sqlite driver for configured metering journals
)

type meteringRuntime struct {
	Recorder     metering.Recorder
	StoreBacking string
	checkReady   func(context.Context) error
}

func buildMeteringRuntime(owner *processResourceOwner, parent context.Context, cfg *config.Config, now func() time.Time, registry *db.PoolRegistry, migrator *dualPlaneMigrator) (*meteringRuntime, error) {
	if cfg == nil || !cfg.Metering.Enabled {
		return nil, nil
	}
	if owner == nil {
		return nil, fmt.Errorf("runtimebundle: nil process owner")
	}
	if parent == nil {
		parent = context.Background()
	}
	if now == nil {
		now = time.Now
	}
	storeName := strings.ToLower(strings.TrimSpace(cfg.Metering.Journal.Store))
	switch storeName {
	case "", "memory":
		store, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "metering-memory", Now: now})
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: metering memory: %w", err)
		}
		owner.Own(store.Close)
		return &meteringRuntime{
			Recorder:     store,
			StoreBacking: "memory",
			checkReady:   store.CheckReadiness,
		}, nil
	case "sqlite", "postgres":
		rec, backing, checkReady, err := openDurableMeteringJournal(owner, parent, cfg, now, registry, migrator)
		if err != nil {
			return nil, err
		}
		return &meteringRuntime{
			Recorder:     rec,
			StoreBacking: backing,
			checkReady:   checkReady,
		}, nil
	default:
		return nil, fmt.Errorf("runtimebundle: metering.journal.store %q is invalid", cfg.Metering.Journal.Store)
	}
}

func openDurableMeteringJournal(owner *processResourceOwner, parent context.Context, cfg *config.Config, now func() time.Time, registry *db.PoolRegistry, migrator *dualPlaneMigrator) (metering.Recorder, string, func(context.Context) error, error) {
	if owner == nil {
		return nil, "", nil, fmt.Errorf("runtimebundle: nil process owner")
	}
	store := strings.ToLower(strings.TrimSpace(cfg.Metering.Journal.Store))
	switch store {
	case "sqlite":
		path := strings.TrimSpace(cfg.Metering.Journal.SQLitePath)
		dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate"
		sqlDB, err := sql.Open("sqlite", dsn)
		if err == nil {
			err = sqlDB.PingContext(parent)
		}
		if err != nil {
			if sqlDB != nil {
				_ = sqlDB.Close()
			}
			return nil, "", nil, fmt.Errorf("runtimebundle: metering journal sqlite open: %w", err)
		}
		sqlDB.SetMaxOpenConns(1)
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			_ = sqlDB.Close()
			return nil, "", nil, fmt.Errorf("runtimebundle: metering journal sqlite bun: %w", err)
		}
		impl, err := journalstore.NewDurableStore(parent, bunDB, journalstore.DurableConfig{StoreID: "metering-sqlite", Now: now})
		if err != nil {
			wrapped := fmt.Errorf("runtimebundle: metering journal schema: %w", err)
			if cerr := bunDB.Close(); cerr != nil {
				return nil, "", nil, errors.Join(wrapped, fmt.Errorf("runtimebundle: metering journal close after schema error: %w", cerr))
			}
			return nil, "", nil, wrapped
		}
		owner.Own(impl.Close)
		return impl, store, impl.CheckReadiness, nil
	case "postgres":
		poolCfg, err := config.ParseDatabasePoolSettings(cfg.Database)
		if err != nil {
			return nil, "", nil, fmt.Errorf("runtimebundle: metering journal postgres pool: %w", err)
		}
		child, cancel := context.WithTimeout(parent, db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		pool := db.PoolSettings{
			MaxOpenConns:    poolCfg.MaxOpenConns,
			MaxIdleConns:    poolCfg.MaxIdleConns,
			ConnMaxLifetime: poolCfg.ConnMaxLifetime,
			ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
		}
		impl, err := acquireOwnedProcess(child, owner, func(ctx context.Context) (*journalstore.DurableStore, func() error, error) {
			return openPostgresStore(ctx, cfg.Metering.Journal.PostgresDSN, pool, cfg.Database, registry, migrator, postgresStoreLifecycle[*journalstore.DurableStore]{
				Migrate: journalstore.Migrate,
				Verify:  journalstore.VerifySchema,
				Open: func(ctx context.Context, handle *bun.DB) (*journalstore.DurableStore, error) {
					return journalstore.OpenStore(ctx, handle, journalstore.DurableConfig{StoreID: "metering-postgres", Now: now})
				},
				Close: (*journalstore.DurableStore).Close,
			})
		})
		if err != nil {
			return nil, "", nil, fmt.Errorf("runtimebundle: metering journal postgres: %w", err)
		}
		return impl, store, impl.CheckReadiness, nil
	default:
		return nil, "", nil, fmt.Errorf("runtimebundle: metering journal store %q is invalid", cfg.Metering.Journal.Store)
	}
}
