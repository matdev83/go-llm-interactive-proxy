package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	BaseURL        string `yaml:"base_url"`
	APIKey         string `yaml:"api_key"`
	HTTPTimeout    string `yaml:"http_timeout"`
	AppURLMode     string `yaml:"app_url_mode"`
	AppURLValue    string `yaml:"app_url_value"`
	AppTitleMode   string `yaml:"app_title_mode"`
	AppTitleValue  string `yaml:"app_title_value"`
	StaticReferer  string `yaml:"static_referer"`
	StaticTitle    string `yaml:"static_title"`
	LegacyAppURL   bool   `yaml:"legacy_app_url"`
	LegacyAppTitle bool   `yaml:"legacy_app_title"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("openrouter: config: %w", err)
		}
	}
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultURL
	}
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.StaticReferer = strings.TrimSpace(cfg.StaticReferer)
	cfg.StaticTitle = strings.TrimSpace(cfg.StaticTitle)
	return cfg, nil
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := 60 * time.Second
	if strings.TrimSpace(c.HTTPTimeout) != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("openrouter: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
