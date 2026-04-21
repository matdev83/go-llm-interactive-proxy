package config

import (
	"fmt"
	"strings"
	"time"
)

// Validate checks plugin identity rules and continuity/store consistency after decoding.
func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config: nil")
	}
	if err := validatePluginSlice("plugins.frontends", cfg.Plugins.Frontends); err != nil {
		return err
	}
	if err := validatePluginSlice("plugins.backends", cfg.Plugins.Backends); err != nil {
		return err
	}
	if err := validatePluginSlice("plugins.features", cfg.Plugins.Features); err != nil {
		return err
	}
	if err := validateContinuityStores(cfg); err != nil {
		return err
	}
	return validateRoutingHealth(cfg)
}

func validateRoutingHealth(cfg *Config) error {
	cb := cfg.Routing.Health.CircuitBreaker
	if !cb.Enabled {
		return nil
	}
	if cb.FailureThreshold < 1 {
		return fmt.Errorf("routing.health.circuit_breaker: failure_threshold must be >= 1 when enabled")
	}
	raw := strings.TrimSpace(cb.OpenFor)
	if raw == "" {
		return nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("routing.health.circuit_breaker.open_for: %w", err)
	}
	if d <= 0 {
		return fmt.Errorf("routing.health.circuit_breaker.open_for: must be a positive duration")
	}
	return nil
}

func validatePluginSlice(section string, rows []PluginConfig) error {
	seen := make(map[string]struct{})
	for _, p := range rows {
		id := p.InstanceID()
		if id == "" {
			return fmt.Errorf("%s: plugin row requires non-empty id", section)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%s: duplicate plugin instance id %q", section, id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(p.FactoryID()) == "" {
			return fmt.Errorf("%s: plugin %q missing factory kind (set kind or id)", section, id)
		}
	}
	return nil
}

func validateContinuityStores(cfg *Config) error {
	store := strings.ToLower(strings.TrimSpace(cfg.Continuity.Store))
	if cfg.Continuity.InMemory {
		store = "memory"
	}
	if store == "" {
		store = "memory"
	}
	if store != "sqlite" {
		return nil
	}
	if strings.TrimSpace(cfg.Continuity.TTL) != "" {
		return fmt.Errorf("continuity: ttl is not supported for sqlite store (memory-only); remove ttl or use store: memory")
	}
	if cfg.Continuity.MaxLegs != 0 {
		return fmt.Errorf("continuity: max_legs is not supported for sqlite store (memory-only); remove max_legs or use store: memory")
	}
	return nil
}
