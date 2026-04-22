package runtimebundle_test

import (
	"log/slog"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func TestBuild_circuitBreakerDisabledUsesEmptyHealth(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Executor.CandidateHealth.(*policy.CircuitBreaker); ok {
		t.Fatal("expected no circuit breaker when disabled")
	}
}

func TestBuild_circuitBreakerEnabledWiresPolicy(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			MaxAttempts: 3,
			Health: config.RoutingHealthConfig{
				CircuitBreaker: config.CircuitBreakerConfig{
					Enabled:          true,
					FailureThreshold: 3,
					OpenFor:          "5s",
				},
			},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Executor.CandidateHealth.(*policy.CircuitBreaker); !ok {
		t.Fatalf("want *policy.CircuitBreaker, got %T", b.Executor.CandidateHealth)
	}
}

func TestBuild_routeObserverUsesSlogWhenLoggerSet(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "x", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	log := slog.New(slog.DiscardHandler)
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), log, &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	if b.Executor.RouteObserver == nil {
		t.Fatal("expected RouteObserver")
	}
}
