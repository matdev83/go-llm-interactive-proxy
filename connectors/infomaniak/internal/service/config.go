package service

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultHTTPTimeout = 60 * time.Second
	DefaultOrigin      = "https://api.infomaniak.com"
)

type Config struct {
	ProductID   string
	APIOrigin   string
	HTTPTimeout string
}

type rawConfig struct {
	ProductID   any    `yaml:"product_id"`
	APIOrigin   string `yaml:"api_origin"`
	HTTPTimeout string `yaml:"http_timeout"`

	// Secrets in YAML are strictly forbidden and rejected if present.
	APIKey   string `yaml:"api_key"`
	Token    string `yaml:"token"`
	APIToken string `yaml:"api_token"`
	Secret   string `yaml:"secret"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var rc rawConfig
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &rc); err != nil {
			return Config{}, fmt.Errorf("infomaniak-ai: config: %w", err)
		}
	}

	// Reject literal secrets in YAML
	if strings.TrimSpace(rc.APIKey) != "" ||
		strings.TrimSpace(rc.Token) != "" ||
		strings.TrimSpace(rc.APIToken) != "" ||
		strings.TrimSpace(rc.Secret) != "" {
		return Config{}, fmt.Errorf("infomaniak-ai: literal secrets in configuration YAML are forbidden; supply via ConfigureRequest.Secrets")
	}

	productID, err := parseProductID(rc.ProductID)
	if err != nil {
		return Config{}, err
	}

	apiOrigin := strings.TrimSpace(rc.APIOrigin)
	if strings.Contains(strings.ToLower(apiOrigin), "/compat") {
		return Config{}, fmt.Errorf("infomaniak-ai: /compat endpoint is forbidden")
	}

	return Config{
		ProductID:   productID,
		APIOrigin:   apiOrigin,
		HTTPTimeout: strings.TrimSpace(rc.HTTPTimeout),
	}, nil
}

func parseProductID(v any) (string, error) {
	if v == nil {
		return "", fmt.Errorf("infomaniak-ai: product_id is required")
	}
	var s string
	switch val := v.(type) {
	case int:
		if val <= 0 {
			return "", fmt.Errorf("infomaniak-ai: product_id must be positive, got %d", val)
		}
		s = strconv.Itoa(val)
	case int64:
		if val <= 0 {
			return "", fmt.Errorf("infomaniak-ai: product_id must be positive, got %d", val)
		}
		s = strconv.FormatInt(val, 10)
	case uint64:
		if val == 0 {
			return "", fmt.Errorf("infomaniak-ai: product_id must be positive, got 0")
		}
		s = strconv.FormatUint(val, 10)
	case string:
		s = strings.TrimSpace(val)
		if s == "" {
			return "", fmt.Errorf("infomaniak-ai: product_id is required")
		}
		for _, r := range s {
			if r < '0' || r > '9' {
				return "", fmt.Errorf("infomaniak-ai: product_id must contain only digits, got %q", s)
			}
		}
		num, err := strconv.ParseInt(s, 10, 64)
		if err != nil || num <= 0 {
			return "", fmt.Errorf("infomaniak-ai: product_id must be positive, got %q", s)
		}
	default:
		return "", fmt.Errorf("infomaniak-ai: product_id must be an integer or string of digits")
	}
	return s, nil
}

func (c Config) BaseURL() string {
	if c.ProductID == "" {
		return ""
	}
	origin := strings.TrimRight(strings.TrimSpace(c.APIOrigin), "/")
	if origin == "" {
		origin = DefaultOrigin
	}
	return fmt.Sprintf("%s/2/ai/%s/openai/v1", origin, c.ProductID)
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("infomaniak-ai: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
