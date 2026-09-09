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
	AccountID   string `yaml:"account_id"`
	GatewayID   string `yaml:"gateway_id"`
	APIOrigin   string `yaml:"api_origin"`
	HTTPTimeout string `yaml:"http_timeout"`

	// Secrets in YAML are strictly forbidden and rejected if present.
	APIToken    string `yaml:"api_token"`
	APIKey      string `yaml:"api_key"`
	Token       string `yaml:"token"`
	BearerToken string `yaml:"bearer_token"`
	Secret      string `yaml:"secret"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("cloudflare: config: %w", err)
		}
	}

	// Reject literal secrets in YAML
	if strings.TrimSpace(cfg.APIToken) != "" ||
		strings.TrimSpace(cfg.APIKey) != "" ||
		strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.BearerToken) != "" ||
		strings.TrimSpace(cfg.Secret) != "" {
		return Config{}, fmt.Errorf("cloudflare: literal secrets in configuration YAML are forbidden; supply via ConfigureRequest.Secrets")
	}

	cfg.AccountID = strings.TrimSpace(cfg.AccountID)
	cfg.GatewayID = strings.TrimSpace(cfg.GatewayID)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	if strings.Contains(strings.ToLower(cfg.APIOrigin), "/compat") {
		return Config{}, fmt.Errorf("cloudflare: /compat endpoint is forbidden for ordinary calls")
	}

	return cfg, nil
}

func (c Config) BaseURL() string {
	origin := strings.TrimRight(strings.TrimSpace(c.APIOrigin), "/")
	if origin == "" {
		origin = DefaultOrigin
	}
	return fmt.Sprintf("%s/client/v4/accounts/%s/ai/v1", origin, strings.TrimSpace(c.AccountID))
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("cloudflare: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
