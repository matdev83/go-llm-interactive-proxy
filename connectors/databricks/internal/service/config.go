package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultHTTPTimeout = 60 * time.Second
)

type Config struct {
	Host            string `yaml:"host"`
	ServingEndpoint string `yaml:"serving_endpoint"`
	APIOrigin       string `yaml:"api_origin"`
	CredentialMode  string `yaml:"credential_mode"`
	HTTPTimeout     string `yaml:"http_timeout"`

	// Secrets in YAML are strictly forbidden and rejected if present.
	Token    string `yaml:"token"`
	APIToken string `yaml:"api_token"`
	PAT      string `yaml:"pat"`
	APIKey   string `yaml:"api_key"`
	Secret   string `yaml:"secret"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("databricks-ai: config: %w", err)
		}
	}

	// Reject literal secrets in YAML
	if strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.APIToken) != "" ||
		strings.TrimSpace(cfg.PAT) != "" ||
		strings.TrimSpace(cfg.APIKey) != "" ||
		strings.TrimSpace(cfg.Secret) != "" {
		return Config{}, fmt.Errorf("databricks-ai: literal secrets in configuration YAML are forbidden; supply via ConfigureRequest.Secrets")
	}

	cfg.Host = normalizeHost(cfg.Host)
	cfg.ServingEndpoint = strings.TrimSpace(cfg.ServingEndpoint)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.CredentialMode = strings.TrimSpace(cfg.CredentialMode)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	if strings.Contains(strings.ToLower(cfg.Host), "/compat") ||
		strings.Contains(strings.ToLower(cfg.APIOrigin), "/compat") {
		return Config{}, fmt.Errorf("databricks-ai: /compat endpoint is forbidden")
	}

	return cfg, nil
}

func normalizeHost(host string) string {
	h := strings.TrimSpace(host)
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	h = strings.TrimRight(h, "/")
	return h
}

func (c Config) BaseURL() string {
	origin := strings.TrimRight(strings.TrimSpace(c.APIOrigin), "/")
	if origin == "" {
		if c.Host != "" {
			origin = "https://" + c.Host
		}
	}
	if origin == "" {
		return ""
	}
	return origin + "/ai-gateway/mlflow/v1"
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("databricks-ai: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
