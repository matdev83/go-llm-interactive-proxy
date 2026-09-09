package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Model       string `yaml:"model"`
	ModelID     string `yaml:"model_id"`
	APIOrigin   string `yaml:"api_origin"`
	HTTPTimeout string `yaml:"http_timeout"`

	// Secrets in YAML are strictly forbidden and rejected if present.
	APIKey  string `yaml:"api_key"`
	APIKey2 string `yaml:"apikey"`
	Token   string `yaml:"token"`
	Secret  string `yaml:"secret"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		var rawMap map[string]any
		if err := yaml.Unmarshal(raw, &rawMap); err != nil {
			return Config{}, fmt.Errorf("cohere: config: %w", err)
		}
		for k := range rawMap {
			lower := strings.ToLower(strings.ReplaceAll(k, "-", "_"))
			if lower == "api_key" || lower == "apikey" || lower == "token" || lower == "secret" {
				return Config{}, fmt.Errorf("cohere: literal secrets in configuration YAML are forbidden; supply api_key via ConfigureRequest.Secrets")
			}
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("cohere: config: %w", err)
		}
	}

	// Double check struct fields for literal secrets
	if strings.TrimSpace(cfg.APIKey) != "" ||
		strings.TrimSpace(cfg.APIKey2) != "" ||
		strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.Secret) != "" {
		return Config{}, fmt.Errorf("cohere: literal secrets in configuration YAML are forbidden; supply api_key via ConfigureRequest.Secrets")
	}

	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.ModelID = strings.TrimSpace(cfg.ModelID)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	return cfg, nil
}

func (c Config) DefaultModel() string {
	if c.Model != "" {
		return c.Model
	}
	return c.ModelID
}

func (c Config) Origin() string {
	if c.APIOrigin != "" {
		return strings.TrimRight(c.APIOrigin, "/")
	}
	return DefaultOrigin
}

func (c Config) HTTPClient() (*http.Client, error) {
	timeout := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		d, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("cohere: invalid http_timeout %q: %w", c.HTTPTimeout, err)
		}
		timeout = d
	}
	return &http.Client{Timeout: timeout}, nil
}
