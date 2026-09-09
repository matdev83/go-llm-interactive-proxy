package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ResourceGroup     string `yaml:"resource_group"`
	InferenceContract string `yaml:"inference_contract"`
	DeploymentID      string `yaml:"deployment_id"`
	APIOrigin         string `yaml:"api_origin"`
	OAuthOrigin       string `yaml:"oauth_origin"`
	HTTPTimeout       string `yaml:"http_timeout"`

	// Secrets in YAML are strictly forbidden and rejected if present.
	ServiceKey    string `yaml:"service_key"`
	ServiceKey2   string `yaml:"servicekey"`
	ClientSecret  string `yaml:"clientsecret"`
	ClientSecret2 string `yaml:"client_secret"`
	ClientID      string `yaml:"clientid"`
	ClientID2     string `yaml:"client_id"`
	Token         string `yaml:"token"`
	Secret        string `yaml:"secret"`
	APIKey        string `yaml:"api_key"`
	APIKey2       string `yaml:"apikey"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		var rawMap map[string]any
		if err := yaml.Unmarshal(raw, &rawMap); err != nil {
			return Config{}, fmt.Errorf("sapaicore: config: %w", err)
		}
		for k := range rawMap {
			lower := strings.ToLower(strings.ReplaceAll(k, "-", "_"))
			if lower == "service_key" || lower == "servicekey" ||
				lower == "clientsecret" || lower == "client_secret" ||
				lower == "clientid" || lower == "client_id" ||
				lower == "token" || lower == "secret" ||
				lower == "api_key" || lower == "apikey" ||
				lower == "serviceurls" || lower == "ai_api_url" {
				return Config{}, fmt.Errorf("sapaicore: literal secrets in configuration YAML are forbidden; supply service_key via ConfigureRequest.Secrets")
			}
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("sapaicore: config: %w", err)
		}
	}

	// Double check struct fields for literal secrets
	if strings.TrimSpace(cfg.ServiceKey) != "" ||
		strings.TrimSpace(cfg.ServiceKey2) != "" ||
		strings.TrimSpace(cfg.ClientSecret) != "" ||
		strings.TrimSpace(cfg.ClientSecret2) != "" ||
		strings.TrimSpace(cfg.ClientID) != "" ||
		strings.TrimSpace(cfg.ClientID2) != "" ||
		strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.Secret) != "" ||
		strings.TrimSpace(cfg.APIKey) != "" ||
		strings.TrimSpace(cfg.APIKey2) != "" {
		return Config{}, fmt.Errorf("sapaicore: literal secrets in configuration YAML are forbidden; supply service_key via ConfigureRequest.Secrets")
	}

	cfg.ResourceGroup = strings.TrimSpace(cfg.ResourceGroup)
	if cfg.ResourceGroup == "" {
		return Config{}, fmt.Errorf("sapaicore: resource_group is required")
	}

	cfg.InferenceContract = strings.TrimSpace(strings.ToLower(cfg.InferenceContract))
	if cfg.InferenceContract == "" {
		return Config{}, fmt.Errorf("sapaicore: inference_contract is required")
	}
	if cfg.InferenceContract != InferenceContractOpenAIChat {
		return Config{}, fmt.Errorf("sapaicore: unsupported inference_contract %q (expected %q)", cfg.InferenceContract, InferenceContractOpenAIChat)
	}

	cfg.DeploymentID = strings.TrimSpace(cfg.DeploymentID)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.OAuthOrigin = strings.TrimSpace(cfg.OAuthOrigin)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	return cfg, nil
}

func (c Config) AIAPIOrigin(serviceKeyAIURL string) string {
	if c.APIOrigin != "" {
		return strings.TrimRight(c.APIOrigin, "/")
	}
	return strings.TrimRight(serviceKeyAIURL, "/")
}

func (c Config) OAuthURL(serviceKeyAuthURL string) string {
	origin := serviceKeyAuthURL
	if c.OAuthOrigin != "" {
		origin = c.OAuthOrigin
	}
	origin = strings.TrimRight(origin, "/")
	if !strings.HasSuffix(origin, "/oauth/token") {
		origin += "/oauth/token"
	}
	return origin
}

func (c Config) HTTPClient() (*http.Client, error) {
	timeout := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		d, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("sapaicore: invalid http_timeout %q: %w", c.HTTPTimeout, err)
		}
		timeout = d
	}
	return &http.Client{Timeout: timeout}, nil
}

type ServiceKey struct {
	ClientID     string `json:"clientid"`
	ClientSecret string `json:"clientsecret"`
	URL          string `json:"url"`
	ServiceURLs  struct {
		AIAPIURL string `json:"AI_API_URL"`
	} `json:"serviceurls"`
}

func ParseServiceKey(raw []byte) (ServiceKey, error) {
	if len(raw) == 0 {
		return ServiceKey{}, fmt.Errorf("sapaicore: missing required service_key in secrets")
	}
	var sk ServiceKey
	if err := json.Unmarshal(raw, &sk); err != nil {
		return ServiceKey{}, fmt.Errorf("sapaicore: invalid service_key JSON")
	}
	sk.ClientID = strings.TrimSpace(sk.ClientID)
	sk.ClientSecret = strings.TrimSpace(sk.ClientSecret)
	sk.URL = strings.TrimSpace(sk.URL)
	sk.ServiceURLs.AIAPIURL = strings.TrimSpace(sk.ServiceURLs.AIAPIURL)

	if sk.ClientID == "" || sk.ClientSecret == "" || sk.URL == "" || sk.ServiceURLs.AIAPIURL == "" {
		return ServiceKey{}, fmt.Errorf("sapaicore: invalid service_key: missing required fields (clientid, clientsecret, url, serviceurls.AI_API_URL)")
	}
	return sk, nil
}
