package config_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestEffectiveTransportFallbackPolicy_defaultsCompatibility(t *testing.T) {
	t.Parallel()

	got := config.EffectiveTransportFallbackPolicy(nil)
	if got != lipapi.TransportFallbackCompatibility {
		t.Fatalf("got %q", got)
	}
}

func TestEffectiveTransportFallbackPolicy_exactFromRouting(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Routing: config.RoutingConfig{
			Transport: config.RoutingTransportConfig{
				FallbackPolicy: "exact",
			},
		},
	}
	got := config.EffectiveTransportFallbackPolicy(cfg)
	if got != lipapi.TransportFallbackExact {
		t.Fatalf("got %q", got)
	}
}
