package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	BaseURL     string `yaml:"base_url"`
	APIKey      string `yaml:"api_key"`
	HTTPTimeout string `yaml:"http_timeout"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("%s: config: %w", "nvidia", err)
		}
	}
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultURL
	}
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.APIKey == "" && "" != "" {
		cfg.APIKey = ""
	}
	return cfg, nil
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := 60 * time.Second
	if strings.TrimSpace(c.HTTPTimeout) != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("%s: http_timeout: %w", "nvidia", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
