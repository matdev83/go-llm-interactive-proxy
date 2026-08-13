package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultHTTPTimeout = 60 * time.Second

type Config struct {
	BaseURL     string `yaml:"base_url"`
	APIKey      string `yaml:"api_key"`
	HTTPTimeout string `yaml:"http_timeout"`
	ModelsURL   string `yaml:"models_url"`
	NativeRoot  string `yaml:"native_root"`
}

func ParseConfigYAML(kind string, raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("%s: config: %w", kind, err)
		}
	}
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.ModelsURL = strings.TrimSpace(cfg.ModelsURL)
	cfg.NativeRoot = strings.TrimSpace(cfg.NativeRoot)
	switch kind {
	case FactoryKindCloud:
		if cfg.BaseURL == "" {
			cfg.BaseURL = DefaultCloudURL
		}
		if cfg.ModelsURL == "" {
			cfg.ModelsURL = DefaultCloudTags
		}
	default:
		if cfg.BaseURL == "" {
			cfg.BaseURL = DefaultLocalURL
		}
		if cfg.APIKey == "" {
			cfg.APIKey = DummyLocalKey
		}
	}
	return cfg, nil
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if strings.TrimSpace(c.HTTPTimeout) != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("ollama: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}

func nativeRootFromBase(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	base = strings.TrimSuffix(base, "/v1")
	return base
}
