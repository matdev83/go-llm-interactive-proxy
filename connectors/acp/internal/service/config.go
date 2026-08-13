package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const FactoryKind = "acp"
const DefaultHTTPTimeout = 60 * time.Second

type Config struct {
	BaseURL string `yaml:"base_url"`
	// HTTPTimeout is optional; when set, Configure builds a caller-owned client.
	HTTPTimeout string `yaml:"http_timeout"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) == 0 {
		return Config{}, fmt.Errorf("acp: base_url is required")
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("acp: config yaml: %w", err)
	}
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	if cfg.BaseURL == "" {
		return Config{}, fmt.Errorf("acp: base_url is required")
	}
	return cfg, nil
}

func (c Config) HTTPClient() (*http.Client, error) {
	to := DefaultHTTPTimeout
	if s := strings.TrimSpace(c.HTTPTimeout); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, fmt.Errorf("acp: http_timeout: %w", err)
		}
		to = d
	}
	return &http.Client{Timeout: to}, nil
}
