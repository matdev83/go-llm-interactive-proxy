package config

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"gopkg.in/yaml.v3"
)

var backendDiscoveryKnownKeys = map[string]struct{}{
	"enabled":          {},
	"paths":            {},
	"strict":           {},
	"development_mode": {},
}

// UnmarshalYAML rejects unknown keys under plugins.backend_discovery.
func (c *BackendDiscoveryConfig) UnmarshalYAML(value *yaml.Node) error {
	decoded, err := DecodeBackendDiscovery(*value)
	if err != nil {
		return err
	}
	*c = decoded
	return nil
}

// DecodeBackendDiscovery strictly decodes a backend_discovery mapping and rejects unknown keys.
func DecodeBackendDiscovery(n yaml.Node) (BackendDiscoveryConfig, error) {
	root := n
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return BackendDiscoveryConfig{}, nil
		}
		root = *root.Content[0]
	}
	if root.Kind == 0 || (root.Kind == yaml.ScalarNode && (root.Tag == "!!null" || strings.TrimSpace(root.Value) == "" || root.Value == "null")) {
		return BackendDiscoveryConfig{}, nil
	}
	if root.Kind != yaml.MappingNode {
		return BackendDiscoveryConfig{}, fmt.Errorf("plugins.backend_discovery: must be a mapping")
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k := root.Content[i].Value
		if _, ok := backendDiscoveryKnownKeys[k]; !ok {
			return BackendDiscoveryConfig{}, fmt.Errorf("plugins.backend_discovery: unknown config key %q", k)
		}
	}
	var raw struct {
		Enabled         bool     `yaml:"enabled"`
		Paths           []string `yaml:"paths"`
		Strict          bool     `yaml:"strict"`
		DevelopmentMode bool     `yaml:"development_mode"`
	}
	if err := root.Decode(&raw); err != nil {
		return BackendDiscoveryConfig{}, fmt.Errorf("plugins.backend_discovery: %w", err)
	}
	return BackendDiscoveryConfig{
		Enabled:         raw.Enabled,
		Paths:           append([]string(nil), raw.Paths...),
		Strict:          raw.Strict,
		DevelopmentMode: raw.DevelopmentMode,
	}, nil
}

// ValidateBackendDiscovery enforces path and production development_mode rules.
func ValidateBackendDiscovery(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config: nil")
	}
	bd := cfg.Plugins.BackendDiscovery
	for i, p := range bd.Paths {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("plugins.backend_discovery.paths[%d]: empty path", i)
		}
	}
	if !bd.DevelopmentMode {
		return nil
	}
	if len(bd.Paths) == 0 {
		return fmt.Errorf("plugins.backend_discovery.paths: required when development_mode is true")
	}
	mode, err := cfg.EffectiveAccessMode()
	if err != nil {
		return fmt.Errorf("plugins.backend_discovery: %w", err)
	}
	if mode == accessmode.ModeMultiUser {
		return fmt.Errorf("plugins.backend_discovery.development_mode: not allowed when access.mode is multi_user")
	}
	return nil
}
