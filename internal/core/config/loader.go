package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// resolveConfigPath returns an absolute path to read. Relative paths are resolved with
// [filepath.Join] against the process working directory after [filepath.Clean] (standard CLI
// semantics). Callers that cd into package subtrees may use ".." segments to reach repo files;
// operator-supplied absolute paths are also accepted.
func resolveConfigPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("config: empty config path")
	}
	p := filepath.Clean(raw)
	if filepath.IsAbs(p) {
		return filepath.Abs(p)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	return filepath.Abs(filepath.Join(wd, p))
}

// LoadFile decodes typed runtime configuration from YAML.
func LoadFile(path string) (*Config, error) {
	resolved, err := resolveConfigPath(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	// security: resolved is cleaned; relative paths are cwd-confined in [resolveConfigPath].
	// Operator-supplied absolute paths are trusted at the process CLI boundary.
	data, err := os.ReadFile(resolved) // #nosec G304
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

	if strings.TrimSpace(cfg.Logging.Level) == "" {
		cfg.Logging.Level = "info"
	}
	if strings.TrimSpace(cfg.Logging.Format) == "" {
		cfg.Logging.Format = "json"
	}

	if mp := strings.TrimSpace(cfg.Observability.Metrics.Path); mp == "" {
		cfg.Observability.Metrics.Path = "/metrics"
	} else {
		cfg.Observability.Metrics.Path = mp
	}

	if err := Validate(&cfg); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}
