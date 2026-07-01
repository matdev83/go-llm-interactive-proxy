//go:build integration

package runtimebundle_test

import (
	"strings"
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

	rows := built.ModelRegistry.All()
	if len(rows) == 0 {
		t.Fatal("central registry has no live opencode-zen models")
	}
	enriched := 0
	for _, row := range rows {
		if row.BackendID != "opencode-zen" || row.Kind != "opencode-zen" {
			t.Fatalf("central registry row = %+v, want opencode-zen backend/kind", row)
		}
		if row.NativeID == "" {
			t.Fatalf("central registry row has empty native id: %+v", row)
		}
		vendor, suffix, ok := strings.Cut(row.CanonicalID, "/")
		if !ok || vendor == "" || suffix == "" {
			t.Fatalf("central registry row has non-vendor canonical id: %+v", row)
		}
		if vendor != "unknown" {
			enriched++
		}
	}
	if enriched == 0 {
		t.Fatalf("central registry has no vendor-enriched opencode-zen rows: %+v", rows)
	}
}
