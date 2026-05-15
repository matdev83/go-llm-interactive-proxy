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
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingledger "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/ledger"
	accountingobs "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/observability"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tokenaccounting/ledgerstore"
	tiktokenlocal "github.com/matdev83/go-llm-interactive-proxy/internal/infra/tokenizers/tiktoken"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite" // register sqlite driver for configured token-accounting ledgers
)

type tokenAccountingRuntime struct {
	Counter       *accountingapp.Service
	Preflight     *accountingpreflight.Checker
	StreamUsage   *accountingstream.Reconstructor
	Ledger        accountingledger.Recorder
	Observability *accountingobs.Stats
	Admin         *accountingapp.Service
}

const defaultAccountingCountTimeout = 750 * time.Millisecond

func buildTokenAccountingRuntime(
	parent context.Context,
	cfg *config.Config,
	now func() time.Time,
	backends map[string]execbackend.Backend,
) (*tokenAccountingRuntime, []func() error, error) {
	if cfg == nil || !cfg.Accounting.Enabled {
		return nil, nil, nil
	}
	if parent == nil {
		parent = context.Background()
	}
	if now == nil {
		now = time.Now
	}
	provider, local, err := buildTokenCounters(cfg, backends)
	if err != nil {
		return nil, nil, err
	}
	countTimeout := defaultAccountingCountTimeout
	if raw := strings.TrimSpace(cfg.Accounting.CountTimeout); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: accounting count_timeout: %w", err)
		}
		countTimeout = parsed
	}
	provider = timeoutProviderCounter{inner: provider, timeout: countTimeout}
	if local != nil {
		local = timeoutLocalCounter{inner: local, timeout: countTimeout}
	}
	counter := accountingapp.NewService(accountingapp.ServiceConfig{Mode: accountingMode(cfg.Accounting.Mode)}, provider, local)
	out := &tokenAccountingRuntime{Counter: counter}
	closers := []func() error{}
	out.Preflight = accountingpreflight.NewChecker(counter, accountingpreflight.Config{
		Enabled:              true,
		Mode:                 preflightMode(cfg.Accounting.Preflight.Mode),
		MaxInputTokens:       cfg.Accounting.Preflight.MaxInputTokens,
		MaxOutputTokens:      cfg.Accounting.Preflight.MaxOutputTokens,
		MaxContextTokens:     cfg.Accounting.Preflight.MaxContextTokens,
		ClampMaxOutputTokens: cfg.Accounting.Preflight.ClampMaxOutputTokens,
	})
	out.StreamUsage = accountingstream.New(counter, accountingstream.Config{})
	switch strings.ToLower(strings.TrimSpace(cfg.Accounting.Ledger.Store)) {
	case "", "memory":
		out.Ledger = accountingledger.NewMemoryLedger(accountingledger.Options{Now: now})
	case "sqlite", "postgres":
		ledger, closeFn, err := openDurableAccountingLedger(parent, cfg)
		if err != nil {
			return nil, nil, err
		}
		out.Ledger = ledger
		closers = append(closers, closeFn)
	}
	if cfg.Accounting.Observability.Enabled {
		out.Observability = accountingobs.NewStats()
	}
	if cfg.Accounting.Admin.Enabled {
		out.Admin = counter
	}
	return out, closers, nil
}

func buildTokenCounters(
	cfg *config.Config,
	backends map[string]execbackend.Backend,
) (accountingapp.ProviderCounter, accountingapp.LocalCounter, error) {
	mode := accountingMode(cfg.Accounting.Mode)
	provider := newBackendProviderCounter(backends)
	if mode == accountingapp.ModeProviderOnly && len(provider.counters) == 0 {
		return nil, nil, fmt.Errorf("runtimebundle: accounting provider_required requires at least one backend provider token counter")
	}
	var local accountingapp.LocalCounter
	if mode != accountingapp.ModeProviderOnly {
		counter, err := tiktokenlocal.NewCounter(tiktokenlocal.Config{
			DefaultEncoding: cfg.Accounting.Tokenizer.DefaultEncoding,
			ModelMappings:   cfg.Accounting.Tokenizer.ModelMappings,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: accounting local tokenizer: %w", err)
		}
		local = counter
	}
	return provider, local, nil
}

type backendProviderCounter struct {
	counters map[string]accountingapp.ProviderCounter
}

func newBackendProviderCounter(backends map[string]execbackend.Backend) *backendProviderCounter {
	out := &backendProviderCounter{counters: map[string]accountingapp.ProviderCounter{}}
	for id, be := range backends {
		if be.ProviderCounter != nil {
			out.counters[id] = be.ProviderCounter
		}
	}
	return out
}

func (c *backendProviderCounter) SupportsCount(ctx context.Context, input accountingapp.ProviderCountInput) accountingapp.ProviderSupport {
	counter, ok := c.counters[input.Backend]
	if !ok {
		return accountingapp.ProviderSupport{Status: accountingapp.SupportStatusUnsupported, Message: "backend has no provider token counter"}
	}
	return counter.SupportsCount(ctx, input)
}

func (c *backendProviderCounter) CountText(ctx context.Context, input accountingapp.CountTextInput) (accountingapp.CountResult, error) {
	counter, ok := c.counters[input.Backend]
	if !ok {
		return accountingapp.CountResult{}, accountingapp.ErrProviderUnsupported
	}
	return counter.CountText(ctx, input)
}

func (c *backendProviderCounter) CountCall(ctx context.Context, input accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	counter, ok := c.counters[input.Backend]
	if !ok {
		return accountingapp.CountResult{}, accountingapp.ErrProviderUnsupported
	}
	return counter.CountCall(ctx, input)
}

func (c *backendProviderCounter) CountOutput(ctx context.Context, input accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	counter, ok := c.counters[input.Backend]
	if !ok {
		return accountingapp.CountResult{}, accountingapp.ErrProviderUnsupported
	}
	return counter.CountOutput(ctx, input)
}

func openDurableAccountingLedger(parent context.Context, cfg *config.Config) (accountingledger.Recorder, func() error, error) {
	store := strings.ToLower(strings.TrimSpace(cfg.Accounting.Ledger.Store))
	var bunDB *bun.DB
	var err error
	switch store {
	case "sqlite":
		path := strings.TrimSpace(cfg.Accounting.Ledger.SQLitePath)
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
			return nil, nil, fmt.Errorf("runtimebundle: accounting ledger sqlite open: %w", err)
		}
		bunDB, err = db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			_ = sqlDB.Close()
			return nil, nil, fmt.Errorf("runtimebundle: accounting ledger sqlite bun: %w", err)
		}
	case "postgres":
		poolCfg, err := config.ParseDatabasePoolSettings(cfg.Database)
		if err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: accounting ledger postgres pool: %w", err)
		}
		child, cancel := context.WithTimeout(parent, db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		bunDB, err = db.OpenPostgresBun(child, cfg.Accounting.Ledger.PostgresDSN, db.PoolSettings{
			MaxOpenConns:    poolCfg.MaxOpenConns,
			MaxIdleConns:    poolCfg.MaxIdleConns,
			ConnMaxLifetime: poolCfg.ConnMaxLifetime,
			ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: accounting ledger postgres open: %w", err)
		}
	default:
		return nil, nil, fmt.Errorf("runtimebundle: accounting ledger store %q is invalid", cfg.Accounting.Ledger.Store)
	}
	storeImpl, err := ledgerstore.NewContext(parent, bunDB)
	if err != nil {
		wrapped := fmt.Errorf("runtimebundle: accounting ledger schema: %w", err)
		if cerr := bunDB.Close(); cerr != nil {
			return nil, nil, errors.Join(wrapped, fmt.Errorf("runtimebundle: accounting ledger close after schema error: %w", cerr))
		}
		return nil, nil, wrapped
	}
	return storeImpl, storeImpl.Close, nil
}

func accountingMode(raw string) accountingapp.Mode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "local_only":
		return accountingapp.ModeLocalOnly
	case "provider_required":
		return accountingapp.ModeProviderOnly
	case "advisory", "provider_first", "":
		return accountingapp.ModeProviderFirst
	default:
		return accountingapp.ModeProviderFirst
	}
}

func preflightMode(raw string) accountingpreflight.Mode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "required":
		return accountingpreflight.ModeStrict
	case "advisory", "":
		return accountingpreflight.ModeAdvisory
	default:
		return accountingpreflight.ModeAdvisory
	}
}

var (
	_ accountingpreflight.Counter = (*accountingapp.Service)(nil)
	_ accountingstream.Counter    = (*accountingapp.Service)(nil)
)

type timeoutProviderCounter struct {
	inner   accountingapp.ProviderCounter
	timeout time.Duration
}

func (c timeoutProviderCounter) SupportsCount(ctx context.Context, input accountingapp.ProviderCountInput) accountingapp.ProviderSupport {
	return c.inner.SupportsCount(ctx, input)
}

func (c timeoutProviderCounter) CountText(ctx context.Context, input accountingapp.CountTextInput) (accountingapp.CountResult, error) {
	return withCountTimeout(ctx, c.timeout, func(ctx context.Context) (accountingapp.CountResult, error) {
		return c.inner.CountText(ctx, input)
	})
}

func (c timeoutProviderCounter) CountCall(ctx context.Context, input accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return withCountTimeout(ctx, c.timeout, func(ctx context.Context) (accountingapp.CountResult, error) {
		return c.inner.CountCall(ctx, input)
	})
}

func (c timeoutProviderCounter) CountOutput(ctx context.Context, input accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return withCountTimeout(ctx, c.timeout, func(ctx context.Context) (accountingapp.CountResult, error) {
		return c.inner.CountOutput(ctx, input)
	})
}

type timeoutLocalCounter struct {
	inner   accountingapp.LocalCounter
	timeout time.Duration
}

func (c timeoutLocalCounter) CountText(ctx context.Context, input accountingapp.CountTextInput) (accountingapp.CountResult, error) {
	return withCountTimeout(ctx, c.timeout, func(ctx context.Context) (accountingapp.CountResult, error) {
		return c.inner.CountText(ctx, input)
	})
}

func (c timeoutLocalCounter) CountCall(ctx context.Context, input accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return withCountTimeout(ctx, c.timeout, func(ctx context.Context) (accountingapp.CountResult, error) {
		return c.inner.CountCall(ctx, input)
	})
}

func (c timeoutLocalCounter) CountOutput(ctx context.Context, input accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return withCountTimeout(ctx, c.timeout, func(ctx context.Context) (accountingapp.CountResult, error) {
		return c.inner.CountOutput(ctx, input)
	})
}

func withCountTimeout(ctx context.Context, timeout time.Duration, fn func(context.Context) (accountingapp.CountResult, error)) (accountingapp.CountResult, error) {
	if timeout <= 0 {
		return fn(ctx)
	}
	child, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return fn(child)
}
