package runtimebundle

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	concurrencyapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/concurrencyauthority/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/concurrencyauthority/leasestore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	_ "modernc.org/sqlite" // register sqlite driver for configured lease stores
)

type concurrencyAuthorityRuntime struct {
	Provider             authority.ConcurrencyProvider
	Service              *concurrencyapp.Service
	LeaseTTL             time.Duration
	RenewBefore          time.Duration
	AuxiliaryLeasePolicy string
}

func buildConcurrencyAuthorityRuntime(parent context.Context, cfg *config.Config, log *slog.Logger, testing TestingOptions) (*concurrencyAuthorityRuntime, []func() error, error) {
	if cfg == nil || !cfg.Accounting.Concurrency.Enabled {
		return nil, nil, nil
	}
	if parent == nil {
		parent = context.Background()
	}
	src, err := configsource.New(cfg.Accounting.Concurrency)
	if err != nil {
		return nil, nil, err
	}
	leaseTTL, err := cfg.Accounting.Concurrency.LeaseTTLDuration()
	if err != nil {
		return nil, nil, err
	}
	renewBefore, err := cfg.Accounting.Concurrency.RenewBeforeDuration()
	if err != nil {
		return nil, nil, err
	}
	store, closers, err := buildConcurrencyLeaseStore(parent, cfg, log, testing)
	if err != nil {
		return nil, nil, err
	}
	clock := concurrencyClockFromTesting(testing)
	svc := concurrencyapp.NewService(src, store, clock)
	return &concurrencyAuthorityRuntime{
		Provider:             concurrencyapp.NewProvider(svc),
		Service:              svc,
		LeaseTTL:             leaseTTL,
		RenewBefore:          renewBefore,
		AuxiliaryLeasePolicy: strings.TrimSpace(cfg.Accounting.Concurrency.AuxiliaryLeasePolicy),
	}, closers, nil
}

func buildConcurrencyLeaseStore(parent context.Context, cfg *config.Config, log *slog.Logger, testing TestingOptions) (concurrencyapp.LeaseStore, []func() error, error) {
	if override := testing.ConcurrencyLeaseStoreOverride; override != nil {
		return override, nil, nil
	}
	cCfg := cfg.Accounting.Concurrency
	storeID := strings.TrimSpace(cCfg.StoreID)
	if storeID == "" {
		storeID = "default"
	}
	switch strings.ToLower(strings.TrimSpace(cCfg.Store)) {
	case "", "memory":
		return leasestore.NewMemory(leasestore.MemoryConfig{StoreID: storeID}), nil, nil
	case "sqlite":
		path := strings.TrimSpace(cCfg.SQLitePath)
		dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate"
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: concurrency lease sqlite open: %w", err)
		}
		sqlDB.SetMaxOpenConns(1)
		if err := sqlDB.PingContext(parent); err != nil {
			_ = sqlDB.Close()
			return nil, nil, fmt.Errorf("runtimebundle: concurrency lease sqlite ping: %w", err)
		}
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			_ = sqlDB.Close()
			return nil, nil, fmt.Errorf("runtimebundle: concurrency lease sqlite bun: %w", err)
		}
		store, err := leasestore.NewDurable(parent, bunDB, leasestore.DurableConfig{StoreID: storeID})
		if err != nil {
			_ = bunDB.Close()
			return nil, nil, fmt.Errorf("runtimebundle: concurrency lease durable sqlite: %w", err)
		}
		_ = log
		return store, []func() error{store.Close}, nil
	case "postgres":
		poolCfg, err := config.ParseDatabasePoolSettings(cfg.Database)
		if err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: concurrency lease postgres pool: %w", err)
		}
		child, cancel := context.WithTimeout(parent, db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		bunDB, err := db.OpenPostgresBun(child, cCfg.PostgresDSN, db.PoolSettings{
			MaxOpenConns:    poolCfg.MaxOpenConns,
			MaxIdleConns:    poolCfg.MaxIdleConns,
			ConnMaxLifetime: poolCfg.ConnMaxLifetime,
			ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: concurrency lease postgres open: %w", err)
		}
		store, err := leasestore.NewDurable(parent, bunDB, leasestore.DurableConfig{StoreID: storeID})
		if err != nil {
			_ = bunDB.Close()
			return nil, nil, fmt.Errorf("runtimebundle: concurrency lease durable postgres: %w", err)
		}
		return store, []func() error{store.Close}, nil
	default:
		return nil, nil, fmt.Errorf("runtimebundle: concurrency lease store %q is invalid", cCfg.Store)
	}
}

func concurrencyClockFromTesting(testing TestingOptions) concurrencyapp.Clock {
	if testing.Clock == nil {
		return nil
	}
	return concurrencyClockFunc(testing.Clock)
}

type concurrencyClockFunc func() time.Time

func (f concurrencyClockFunc) Now() time.Time { return f() }

func attachConcurrencyToAccounting(rt *runtime.AccountingRuntime, conc *concurrencyAuthorityRuntime) {
	if rt == nil || conc == nil {
		return
	}
	rt.ConcurrencyProvider = conc.Provider
	rt.ConcurrencyLeaseTTL = conc.LeaseTTL
	rt.ConcurrencyRenewBefore = conc.RenewBefore
	rt.ConcurrencyAuxiliaryLeasePolicy = conc.AuxiliaryLeasePolicy
}
