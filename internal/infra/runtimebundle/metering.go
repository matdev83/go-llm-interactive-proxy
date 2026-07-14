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
	Recorder metering.Recorder
}

func buildMeteringRuntime(parent context.Context, cfg *config.Config, now func() time.Time) (*meteringRuntime, []func() error, error) {
	if cfg == nil || !cfg.Metering.Enabled {
		return nil, nil, nil
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
			return nil, nil, fmt.Errorf("runtimebundle: metering memory: %w", err)
		}
		return &meteringRuntime{Recorder: store}, []func() error{store.Close}, nil
	case "sqlite", "postgres":
		rec, closeFn, err := openDurableMeteringJournal(parent, cfg, now)
		if err != nil {
			return nil, nil, err
		}
		return &meteringRuntime{Recorder: rec}, []func() error{closeFn}, nil
	default:
		return nil, nil, fmt.Errorf("runtimebundle: metering.journal.store %q is invalid", cfg.Metering.Journal.Store)
	}
}

func openDurableMeteringJournal(parent context.Context, cfg *config.Config, now func() time.Time) (metering.Recorder, func() error, error) {
	store := strings.ToLower(strings.TrimSpace(cfg.Metering.Journal.Store))
	var bunDB *bun.DB
	var err error
	switch store {
	case "sqlite":
		path := strings.TrimSpace(cfg.Metering.Journal.SQLitePath)
		dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
		var sqlDB *sql.DB
		sqlDB, err = sql.Open("sqlite", dsn)
		if err == nil {
			err = sqlDB.PingContext(parent)
		}
		if err != nil {
			if sqlDB != nil {
				_ = sqlDB.Close()
			}
			return nil, nil, fmt.Errorf("runtimebundle: metering journal sqlite open: %w", err)
		}
		bunDB, err = db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			_ = sqlDB.Close()
			return nil, nil, fmt.Errorf("runtimebundle: metering journal sqlite bun: %w", err)
		}
	case "postgres":
		poolCfg, err := config.ParseDatabasePoolSettings(cfg.Database)
		if err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: metering journal postgres pool: %w", err)
		}
		child, cancel := context.WithTimeout(parent, db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		bunDB, err = db.OpenPostgresBun(child, cfg.Metering.Journal.PostgresDSN, db.PoolSettings{
			MaxOpenConns:    poolCfg.MaxOpenConns,
			MaxIdleConns:    poolCfg.MaxIdleConns,
			ConnMaxLifetime: poolCfg.ConnMaxLifetime,
			ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: metering journal postgres open: %w", err)
		}
	default:
		return nil, nil, fmt.Errorf("runtimebundle: metering journal store %q is invalid", cfg.Metering.Journal.Store)
	}
	impl, err := journalstore.NewDurableStore(parent, bunDB, journalstore.DurableConfig{StoreID: "metering-" + store, Now: now})
	if err != nil {
		wrapped := fmt.Errorf("runtimebundle: metering journal schema: %w", err)
		if cerr := bunDB.Close(); cerr != nil {
			return nil, nil, errors.Join(wrapped, fmt.Errorf("runtimebundle: metering journal close after schema error: %w", cerr))
		}
		return nil, nil, wrapped
	}
	return impl, impl.Close, nil
}
