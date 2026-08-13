package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/opencode/internal/catalog"
	"gopkg.in/yaml.v3"
)

const DefaultHTTPTimeout = 60 * time.Second

type Config struct {
	BaseURL     string               `yaml:"base_url"`
	APIKey      string               `yaml:"api_key"`
	HTTPTimeout string               `yaml:"http_timeout"`
	Models      []catalog.ModelEntry `yaml:"-"`
}

type configYAML struct {
	BaseURL     string `yaml:"base_url"`
	APIKey      string `yaml:"api_key"`
	HTTPTimeout string `yaml:"http_timeout"`
	Models      []struct {
		ID           string `yaml:"id"`
		DisplayName  string `yaml:"display_name"`
		Endpoint     string `yaml:"endpoint"`
		AISDKPackage string `yaml:"ai_sdk_package"`
	} `yaml:"models"`
}

func ParseConfigYAML(kind string, raw []byte) (Config, error) {
	var y configYAML
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &y); err != nil {
			return Config{}, fmt.Errorf("opencode: config: %w", err)
		}
	}
	cfg := Config{
		BaseURL:     strings.TrimSpace(y.BaseURL),
		APIKey:      strings.TrimSpace(y.APIKey),
		HTTPTimeout: strings.TrimSpace(y.HTTPTimeout),
	}
	if cfg.BaseURL == "" {
		switch kind {
		case FactoryKindGo:
			cfg.BaseURL = DefaultGoURL
		case FactoryKindZen:
			cfg.BaseURL = DefaultZenURL
		default:
			return Config{}, fmt.Errorf("opencode: unknown factory kind %q", kind)
		}
	}
	for _, row := range y.Models {
		id := strings.TrimSpace(row.ID)
		if id == "" {
			continue
		}
		cfg.Models = append(cfg.Models, catalog.ModelEntry{
			RawID:        id,
			DisplayName:  strings.TrimSpace(row.DisplayName),
			Endpoint:     strings.TrimSpace(row.Endpoint),
			AISDKPackage: strings.TrimSpace(row.AISDKPackage),
		})
	}
	return cfg, nil
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("opencode: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
