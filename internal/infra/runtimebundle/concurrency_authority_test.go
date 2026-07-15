package runtimebundle_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestBuildConcurrencyAuthorityDisabledIsNoop(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), baseAuthorityOptions(t, nil))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() {
		for _, closer := range built.Closers {
			_ = closer()
		}
	})
	if built.Executor.ConcurrencyProvider != nil {
		t.Fatal("concurrency provider must be nil when disabled")
	}
	if built.Executor.RequestCoordinator != nil && built.Executor.RequestCoordinator.Concurrency != nil {
		t.Fatal("request coordinator must not wire concurrency when disabled")
	}
}

func TestBuildConcurrencyAuthorityWiresProvider(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	cfg.Accounting.Concurrency = config.ConcurrencyAuthorityConfig{
		Enabled: true,
		Store:   "memory",
		Rules: []config.ConcurrencyAuthorityRuleConfig{{
			ID:                "max-active",
			Mode:              "strict",
			MaxActiveRequests: 2,
			Match: config.AccountingAuthorityDimensionsConfig{
				Principal: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("alice")},
			},
		}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), baseAuthorityOptions(t, nil))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() {
		for _, closer := range built.Closers {
			_ = closer()
		}
	})
	if built.Executor.ConcurrencyProvider == nil {
		t.Fatal("expected concurrency provider")
	}
	if built.Executor.RequestCoordinator == nil || built.Executor.RequestCoordinator.Concurrency == nil {
		t.Fatal("expected concurrency wired into request coordinator")
	}
	dec, err := built.Executor.ConcurrencyProvider.AdmitLease(context.Background(), authority.LeaseAdmission{
		RequestID: "r1",
		Scope:     scope.PrincipalScopeView{PrincipalID: scope.Known("alice")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != authority.LeaseAllow || dec.LeaseID == "" {
		t.Fatalf("dec=%+v", dec)
	}
	if err := built.Executor.ConcurrencyProvider.ReleaseLease(context.Background(), authority.LeaseRelease{
		LeaseID:   dec.LeaseID,
		RequestID: "r1",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBuildConcurrencyAuthoritySQLite(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	cfg.Accounting.Concurrency = config.ConcurrencyAuthorityConfig{
		Enabled:    true,
		Store:      "sqlite",
		SQLitePath: filepath.Join(t.TempDir(), "leases.db"),
		Rules: []config.ConcurrencyAuthorityRuleConfig{{
			ID:                "max-active",
			MaxActiveRequests: 1,
			Match: config.AccountingAuthorityDimensionsConfig{
				Principal: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("bob")},
			},
		}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), baseAuthorityOptions(t, nil))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() {
		for _, closer := range built.Closers {
			_ = closer()
		}
	})
	if built.Executor.ConcurrencyProvider == nil {
		t.Fatal("expected sqlite concurrency provider")
	}
}
