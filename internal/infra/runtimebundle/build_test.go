package runtimebundle_test

import (
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func TestBuildExecutor_productionClockAndRNG(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := b.Executor
	if b.UpstreamHTTP == nil {
		t.Fatal("expected shared upstream HTTP client")
	}
	if b.PluginRegistry == nil {
		t.Fatal("expected PluginRegistry")
	}
	if ex.Now == nil {
		t.Fatal("expected non-nil Now")
	}
	if ex.Rand == nil {
		t.Fatal("expected non-nil Rand")
	}
	if ex.CandidateHealth == nil {
		t.Fatal("expected CandidateHealth wired")
	}
	if ex.RouteObserver == nil {
		t.Fatal("expected RouteObserver wired")
	}
}

func TestBuild_respectsHTTPClientInBuildOptions(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	custom := &http.Client{}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		HTTPClient:     custom,
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.UpstreamHTTP != custom {
		t.Fatalf("UpstreamHTTP: got %p want %p", b.UpstreamHTTP, custom)
	}
}

func TestBuild_setsEffectiveDefaultRoute_defaultWireModel(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.EffectiveDefaultRoute == "" {
		t.Fatal("EffectiveDefaultRoute should be non-empty")
	}
	want := routing.EffectiveDefaultRouteSelector(cfg, pluginreg.DefaultWireModel)
	if b.EffectiveDefaultRoute != want {
		t.Fatalf("EffectiveDefaultRoute: got %q want %q", b.EffectiveDefaultRoute, want)
	}
}

func TestBuild_respectsWireModelInBuildOptions(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		WireModel:      func(string) string { return "wm-override" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "openai-responses:wm-override"; b.EffectiveDefaultRoute != want {
		t.Fatalf("EffectiveDefaultRoute: got %q want %q", b.EffectiveDefaultRoute, want)
	}
}
