package service

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultHTTPTimeout = 60 * time.Second
)

type Config struct {
	Region              string `yaml:"region"`
	CompartmentID       string `yaml:"compartment_id"`
	ModelID             string `yaml:"model_id"`
	DedicatedEndpointID string `yaml:"dedicated_endpoint_id"`
	APIOrigin           string `yaml:"api_origin"`
	TenancyOCID         string `yaml:"tenancy_ocid"`
	UserOCID            string `yaml:"user_ocid"`
	Fingerprint         string `yaml:"fingerprint"`
	HTTPTimeout         string `yaml:"http_timeout"`

	// Secrets in YAML are strictly forbidden and rejected if present.
	PrivateKey           string `yaml:"private_key"`
	PrivateKeyPEM        string `yaml:"private_key_pem"`
	PrivateKeyPassphrase string `yaml:"private_key_passphrase"`
	Passphrase           string `yaml:"passphrase"`
	Token                string `yaml:"token"`
	APIKey               string `yaml:"api_key"`
	Secret               string `yaml:"secret"`
	Key                  string `yaml:"key"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("oci-generative-ai: config: %w", err)
		}
	}

	// Reject literal secrets in YAML
	if strings.TrimSpace(cfg.PrivateKey) != "" ||
		strings.TrimSpace(cfg.PrivateKeyPEM) != "" ||
		strings.TrimSpace(cfg.PrivateKeyPassphrase) != "" ||
		strings.TrimSpace(cfg.Passphrase) != "" ||
		strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.APIKey) != "" ||
		strings.TrimSpace(cfg.Secret) != "" ||
		strings.TrimSpace(cfg.Key) != "" {
		return Config{}, fmt.Errorf("oci-generative-ai: literal secrets in configuration YAML are forbidden; supply via ConfigureRequest.Secrets")
	}

	cfg.Region = strings.TrimSpace(cfg.Region)
	cfg.CompartmentID = strings.TrimSpace(cfg.CompartmentID)
	cfg.ModelID = strings.TrimSpace(cfg.ModelID)
	cfg.DedicatedEndpointID = strings.TrimSpace(cfg.DedicatedEndpointID)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.TenancyOCID = strings.TrimSpace(cfg.TenancyOCID)
	cfg.UserOCID = strings.TrimSpace(cfg.UserOCID)
	cfg.Fingerprint = strings.TrimSpace(cfg.Fingerprint)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	if cfg.Region == "" {
		return Config{}, fmt.Errorf("oci-generative-ai: region is required")
	}
	if cfg.CompartmentID == "" {
		return Config{}, fmt.Errorf("oci-generative-ai: compartment_id is required")
	}

	return cfg, nil
}

func (c Config) ChatEndpoint() string {
	origin := strings.TrimRight(c.APIOrigin, "/")
	if origin == "" {
		origin = fmt.Sprintf("https://inference.generativeai.%s.oci.oraclecloud.com", c.Region)
	}
	return origin + "/20231130/actions/chat"
}

func (c Config) ManagementEndpoint() string {
	origin := strings.TrimRight(c.APIOrigin, "/")
	if origin == "" {
		origin = fmt.Sprintf("https://generativeai.%s.oci.oraclecloud.com", c.Region)
	}
	return fmt.Sprintf("%s/20231130/models?compartmentId=%s", origin, url.QueryEscape(c.CompartmentID))
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("oci-generative-ai: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
