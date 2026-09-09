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
	Account        string `yaml:"account"`
	Role           string `yaml:"role"`
	APIOrigin      string `yaml:"api_origin"`
	CredentialMode string `yaml:"credential_mode"`
	HTTPTimeout    string `yaml:"http_timeout"`

	// Secrets in YAML are strictly forbidden and rejected if present.
	PAT      string `yaml:"pat"`
	APIToken string `yaml:"api_token"`
	Token    string `yaml:"token"`
	APIKey   string `yaml:"api_key"`
	Secret   string `yaml:"secret"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("snowflake-cortex: config: %w", err)
		}
	}

	// Reject literal secrets in YAML
	if strings.TrimSpace(cfg.PAT) != "" ||
		strings.TrimSpace(cfg.APIToken) != "" ||
		strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.APIKey) != "" ||
		strings.TrimSpace(cfg.Secret) != "" {
		return Config{}, fmt.Errorf("snowflake-cortex: literal secrets in configuration YAML are forbidden; supply via ConfigureRequest.Secrets")
	}

	cfg.Account = normalizeAccount(cfg.Account)
	cfg.Role = strings.TrimSpace(cfg.Role)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.CredentialMode = strings.TrimSpace(cfg.CredentialMode)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	if strings.Contains(strings.ToLower(cfg.Account), "/compat") ||
		strings.Contains(strings.ToLower(cfg.APIOrigin), "/compat") {
		return Config{}, fmt.Errorf("snowflake-cortex: /compat endpoint is forbidden")
	}

	return cfg, nil
}

func normalizeAccount(account string) string {
	acc := strings.TrimSpace(account)
	acc = strings.TrimPrefix(acc, "https://")
	acc = strings.TrimPrefix(acc, "http://")
	acc = strings.TrimRight(acc, "/")
	acc = strings.TrimSuffix(acc, ".snowflakecomputing.com")
	return acc
}

func (c Config) BaseURL() string {
	origin := strings.TrimRight(strings.TrimSpace(c.APIOrigin), "/")
	if origin == "" {
		if c.Account != "" {
			origin = fmt.Sprintf("https://%s.snowflakecomputing.com", c.Account)
		}
	}
	if origin == "" {
		return ""
	}
	return origin + "/api/v2/cortex/v1"
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("snowflake-cortex: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
