package config

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// Config contains only core-owned runtime settings and opaque plugin config payloads.
type Config struct {
	Server      ServerConfig      `yaml:"server"`
	Diagnostics DiagnosticsConfig `yaml:"diagnostics"`
	Routing     RoutingConfig     `yaml:"routing"`
	Continuity  ContinuityConfig  `yaml:"continuity"`
	Hooks       HooksConfig       `yaml:"hooks"`
	Plugins     PluginsConfig     `yaml:"plugins"`
}

// HooksConfig carries core hook-bus tuning (not plugin opaque payloads).
type HooksConfig struct {
	// ToolReactorErrorPolicy is one of: fail_open (default), fail_closed, swallow_event.
	ToolReactorErrorPolicy string `yaml:"tool_reactor_error_policy"`
}

type ServerConfig struct {
	Address string `yaml:"address"`
	// MaxRequestBodyBytes caps HTTP request bodies for bundled frontends. Zero selects
	// each handler's default limit (see internal/plugins/frontends/reqbody).
	MaxRequestBodyBytes int64 `yaml:"max_request_body_bytes"`
}

// EffectiveMaxRequestBodyBytes returns MaxRequestBodyBytes when positive, otherwise zero
// (callers treat zero as "use handler default").
func (s ServerConfig) EffectiveMaxRequestBodyBytes() int64 {
	if s.MaxRequestBodyBytes > 0 {
		return s.MaxRequestBodyBytes
	}
	return 0
}

type DiagnosticsConfig struct {
	Enabled      bool   `yaml:"enabled"`
	HealthPath   string `yaml:"health_path"`
	AttemptsPath string `yaml:"attempts_path"`
	// InventoryPath registers a JSON plugin inventory endpoint when non-empty (e.g. "/debug/inventory").
	InventoryPath string `yaml:"inventory_path"`
	// RouteTracePath registers a JSON ring buffer of recent routing decisions when non-empty.
	RouteTracePath string `yaml:"route_trace_path"`
}

type RoutingConfig struct {
	MaxAttempts int `yaml:"max_attempts"`
	// DefaultRoute is the selector used when the client omits X-LIP-Route (e.g. "openai-responses:gpt-4o-mini").
	DefaultRoute string              `yaml:"default_route"`
	Health       RoutingHealthConfig `yaml:"health"`
}

type RoutingHealthConfig struct {
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
}

type CircuitBreakerConfig struct {
	Enabled          bool   `yaml:"enabled"`
	FailureThreshold int    `yaml:"failure_threshold"`
	OpenFor          string `yaml:"open_for"`
}

type ContinuityConfig struct {
	InMemory bool `yaml:"in_memory"`
	// Store names the continuity backing when InMemory is true. Empty is normalized to "memory" in LoadFile.
	// Use "sqlite" for durable local storage (requires sqlite_path).
	Store string `yaml:"store"`
	// SQLitePath is the database file path when store is "sqlite".
	SQLitePath string `yaml:"sqlite_path"`
	// TTL is in-memory store only (A-leg eviction). Ignored by SQLite until pruning is implemented.
	TTL string `yaml:"ttl"`
	// MaxLegs is in-memory store only when TTL is empty. Ignored by SQLite until pruning exists.
	MaxLegs int `yaml:"max_legs"`
}

type PluginsConfig struct {
	Frontends []PluginConfig `yaml:"frontends"`
	Backends  []PluginConfig `yaml:"backends"`
	Features  []PluginConfig `yaml:"features"`
}

// PluginConfig keeps plugin-private config opaque to the core.
type PluginConfig struct {
	// Kind is the bundled factory id used for registry lookup (e.g. openai-responses).
	// When empty, ID is treated as both factory kind and instance id (legacy single-field configs).
	Kind string `yaml:"kind,omitempty"`
	// ID is the runtime instance id: routing keys, executor backend map keys, and duplicate detection.
	ID      string    `yaml:"id"`
	Enabled bool      `yaml:"enabled"`
	Config  yaml.Node `yaml:"config"`
}

// FactoryID returns the registry/factory identifier for this plugin row.
func (p PluginConfig) FactoryID() string {
	k := strings.TrimSpace(p.Kind)
	if k != "" {
		return k
	}
	return strings.TrimSpace(p.ID)
}

// InstanceID returns the configured runtime instance identifier (never empty for valid configs).
func (p PluginConfig) InstanceID() string {
	return strings.TrimSpace(p.ID)
}
