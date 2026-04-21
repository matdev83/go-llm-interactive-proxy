package routing

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

func TestEffectiveDefaultRouteSelector_explicitYAML(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			DefaultRoute: "stub:custom-model",
		},
	}
	got := EffectiveDefaultRouteSelector(cfg, func(string) string { return "ignored" })
	if got != "stub:custom-model" {
		t.Fatalf("got %q want stub:custom-model", got)
	}
}

func TestEffectiveDefaultRouteSelector_firstEnabledBackend(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "anthropic", Enabled: false},
				{ID: "stub", Enabled: true},
			},
		},
	}
	got := EffectiveDefaultRouteSelector(cfg, func(id string) string {
		if id == "stub" {
			return "m1"
		}
		return "x"
	})
	if got != "stub:m1" {
		t.Fatalf("got %q want stub:m1", got)
	}
}

func TestEffectiveDefaultRouteSelector_fallbackUsesWireModel(t *testing.T) {
	t.Parallel()
	got := EffectiveDefaultRouteSelector(nil, DefaultWireModelForTest)
	if got != "openai-responses:gpt-test" {
		t.Fatalf("got %q", got)
	}
}

func DefaultWireModelForTest(backendID string) string {
	if backendID == "openai-responses" {
		return "gpt-test"
	}
	return "m"
}
