//go:build integration

package runtimebundle_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func TestOpenCodeZenLive_modelsAreVendorEnrichedInCentralRegistry(t *testing.T) {
	if testing.Short() {
		t.Skip("live OpenCode Zen registry test")
	}
	keys := pluginreg.ResolveUpstreamAPIKeysFromEnv()
	if len(keys.OpenCodeZen) == 0 {
		t.Skip("OPENCODE_API_KEY or OPENCODE_ZEN_API_KEY is required")
	}

	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBundleOn(reg, keys); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			MaxAttempts:  1,
			DefaultRoute: "opencode-zen:deepseek/deepseek-v4-flash-free",
		},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{
				{ID: "openai-responses", Enabled: true},
				{ID: "openai-legacy", Enabled: true},
				{ID: "anthropic", Enabled: true},
				{ID: "gemini", Enabled: true},
			},
			Backends: []config.PluginConfig{
				{ID: "opencode-go", Enabled: false},
				{ID: "opencode-zen", Enabled: true},
			},
			Features: []config.PluginConfig{
				{ID: "submit-noop", Enabled: true},
				{ID: "parts-noop", Enabled: true},
				{ID: "tool-reactor-noop", Enabled: true},
			},
		},
	}
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRuntimeBuilt(t, built)

	want := map[string]string{
		"deepseek/deepseek-v4-flash-free": "deepseek-v4-flash-free",
		"xiaomi/mimo-v2.5-free":           "mimo-v2.5-free",
		"alibaba/qwen3.6-plus-free":       "qwen3.6-plus-free",
		"minimax/minimax-m3-free":         "minimax-m3-free",
	}
	for canonicalID, nativeID := range want {
		rows, ok := built.ModelRegistry.Lookup(canonicalID)
		if !ok {
			t.Fatalf("central registry missing %q", canonicalID)
		}
		found := false
		for _, row := range rows {
			if row.BackendID == "opencode-zen" && row.Kind == "opencode-zen" && row.NativeID == nativeID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("central registry rows for %q = %+v, want opencode-zen native %q", canonicalID, rows, nativeID)
		}
	}
}
