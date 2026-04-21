package config

import "gopkg.in/yaml.v3"

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
	DefaultRoute string `yaml:"default_route"`
}

type ContinuityConfig struct {
	InMemory bool `yaml:"in_memory"`
	// Store names the continuity backing when InMemory is true. Empty is normalized to "memory" in LoadFile.
	// Use "sqlite" for durable local storage (requires sqlite_path).
	Store string `yaml:"store"`
	// SQLitePath is the database file path when store is "sqlite".
	SQLitePath string `yaml:"sqlite_path"`
	// TTL is a Go duration string (e.g. "24h") for A-leg eviction; empty disables TTL-based expiry.
	TTL string `yaml:"ttl"`
	// MaxLegs caps concurrent A-leg rows when TTL is empty; zero uses the b2bua default.
	MaxLegs int `yaml:"max_legs"`
}

type PluginsConfig struct {
	Frontends []PluginConfig `yaml:"frontends"`
	Backends  []PluginConfig `yaml:"backends"`
	Features  []PluginConfig `yaml:"features"`
}

// PluginConfig keeps plugin-private config opaque to the core.
type PluginConfig struct {
	ID      string    `yaml:"id"`
	Enabled bool      `yaml:"enabled"`
	Config  yaml.Node `yaml:"config"`
}
