package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadFile decodes typed runtime configuration from YAML.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	if cfg.Server.Address == "" {
		cfg.Server.Address = ":8080"
	}

	if cfg.Diagnostics.HealthPath == "" {
		cfg.Diagnostics.HealthPath = "/healthz"
	}

	if cfg.Diagnostics.AttemptsPath == "" {
		cfg.Diagnostics.AttemptsPath = "/admin/attempts"
	}

	if cfg.Routing.MaxAttempts == 0 {
		cfg.Routing.MaxAttempts = 3
	}

	if cfg.Continuity.InMemory && strings.TrimSpace(cfg.Continuity.Store) == "" {
		cfg.Continuity.Store = "memory"
	}

	return &cfg, nil
}
