package config_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

func TestValidate_rejectsSQLiteWithTTL(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{
			InMemory:   false,
			Store:      "sqlite",
			SQLitePath: ":memory:",
			TTL:        "1h",
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected error for ttl with sqlite store")
	}
}

func TestValidate_rejectsSQLiteWithMaxLegs(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{
			InMemory:   false,
			Store:      "sqlite",
			SQLitePath: ":memory:",
			MaxLegs:    10,
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected error for max_legs with sqlite store")
	}
}

func TestValidate_rejectsMemoryWithNegativeMaxLegs(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{
			InMemory: true,
			MaxLegs:  -1,
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected error for negative max_legs with memory store")
	}
	if !strings.Contains(err.Error(), "max_legs") {
		t.Fatalf("error: %v", err)
	}
}

func TestValidate_allowsMemoryZeroAndPositiveMaxLegs(t *testing.T) {
	t.Parallel()
	for _, max := range []int{0, 42} {
		cfg := &config.Config{
			Continuity: config.ContinuityConfig{
				InMemory: true,
				MaxLegs:  max,
			},
			Plugins: config.PluginsConfig{
				Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
			},
		}
		if err := config.Validate(cfg); err != nil {
			t.Fatalf("max_legs=%d: %v", max, err)
		}
	}
}

func TestValidate_rejectsDuplicatePluginInstanceID(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "dup", Enabled: true},
				{ID: "dup", Enabled: false},
			},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected duplicate instance id error")
	}
}

func TestValidate_rejectsCircuitBreakerEnabledWithZeroThreshold(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			Health: config.RoutingHealthConfig{
				CircuitBreaker: config.CircuitBreakerConfig{
					Enabled:          true,
					FailureThreshold: 0,
				},
			},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected failure_threshold error")
	}
}

func TestValidate_rejectsCircuitBreakerNonPositiveOpenFor(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			Health: config.RoutingHealthConfig{
				CircuitBreaker: config.CircuitBreakerConfig{
					Enabled:          true,
					FailureThreshold: 2,
					OpenFor:          "0s",
				},
			},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected open_for positive duration error")
	}
}

func TestValidate_rejectsCircuitBreakerInvalidOpenFor(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			Health: config.RoutingHealthConfig{
				CircuitBreaker: config.CircuitBreakerConfig{
					Enabled:          true,
					FailureThreshold: 2,
					OpenFor:          "not-a-duration",
				},
			},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected open_for parse error")
	}
}
