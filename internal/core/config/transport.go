package config

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type RoutingTransportConfig struct {
	FallbackPolicy string `yaml:"fallback_policy"`
}

func EffectiveTransportFallbackPolicy(cfg *Config) lipapi.TransportFallbackPolicy {
	if cfg == nil {
		return lipapi.TransportFallbackCompatibility
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Routing.Transport.FallbackPolicy)) {
	case string(lipapi.TransportFallbackExact):
		return lipapi.TransportFallbackExact
	default:
		return lipapi.TransportFallbackCompatibility
	}
}
