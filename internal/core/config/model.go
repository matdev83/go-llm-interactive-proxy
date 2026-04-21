package config

import "gopkg.in/yaml.v3"

// Config contains only core-owned runtime settings and opaque plugin config payloads.
type Config struct {
	Server      ServerConfig      `yaml:"server"`
	Diagnostics DiagnosticsConfig `yaml:"diagnostics"`
	Routing     RoutingConfig     `yaml:"routing"`
	Continuity  ContinuityConfig  `yaml:"continuity"`
	Plugins     PluginsConfig     `yaml:"plugins"`
}

type ServerConfig struct {
	Address string `yaml:"address"`
}

type DiagnosticsConfig struct {
	Enabled      bool   `yaml:"enabled"`
	HealthPath   string `yaml:"health_path"`
	AttemptsPath string `yaml:"attempts_path"`
}

type RoutingConfig struct {
	MaxAttempts int `yaml:"max_attempts"`
	// DefaultRoute is the selector used when the client omits X-LIP-Route (e.g. "openai-responses:gpt-4o-mini").
	DefaultRoute string `yaml:"default_route"`
}

type ContinuityConfig struct {
	InMemory bool `yaml:"in_memory"`
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
