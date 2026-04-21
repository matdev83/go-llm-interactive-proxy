package runtimebundle_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"gopkg.in/yaml.v3"
)

// TestBuild_injectedRegistryOnly builds a runtime using a non-default [pluginreg.Registry] so tests
// can assemble partial bundles without depending on [pluginreg.Default] contents.
func TestBuild_injectedRegistryOnly(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	pluginreg.InstallStandardBackendsOn(reg)

	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{Kind: "openai-responses", ID: "openai-only", Enabled: true, Config: empty},
			},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), nil, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Executor.Backends) != 1 {
		t.Fatalf("backends: got %d want 1", len(b.Executor.Backends))
	}
	if _, ok := b.Executor.Backends["openai-only"]; !ok {
		t.Fatal("missing instance openai-only")
	}
	if b.PluginRegistry != reg {
		t.Fatalf("PluginRegistry: got %p want %p", b.PluginRegistry, reg)
	}
}
