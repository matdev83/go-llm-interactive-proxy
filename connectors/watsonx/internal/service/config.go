package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Region       string `yaml:"region"`
	ProjectID    string `yaml:"project_id"`
	SpaceID      string `yaml:"space_id"`
	ModelID      string `yaml:"model_id"`
	DeploymentID string `yaml:"deployment_id"`
	InferenceAPI string `yaml:"inference_api"`
	APIOrigin    string `yaml:"api_origin"`
	IAMOrigin    string `yaml:"iam_origin"`
	APIVersion   string `yaml:"api_version"`
	HTTPTimeout  string `yaml:"http_timeout"`

	// Secrets in YAML are strictly forbidden and rejected if present.
	APIKey   string `yaml:"api_key"`
	APIKey2  string `yaml:"apikey"`
	IAMToken string `yaml:"iam_token"`
	Token    string `yaml:"token"`
	Secret   string `yaml:"secret"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		var rawMap map[string]any
		if err := yaml.Unmarshal(raw, &rawMap); err != nil {
			return Config{}, fmt.Errorf("watsonx: config: %w", err)
		}
		for k := range rawMap {
			lower := strings.ToLower(k)
			if lower == "api_key" || lower == "apikey" || lower == "iam_token" || lower == "token" || lower == "secret" {
				return Config{}, fmt.Errorf("watsonx: literal secrets in configuration YAML are forbidden; supply via ConfigureRequest.Secrets")
			}
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("watsonx: config: %w", err)
		}
	}

	// Double check struct fields for literal secrets
	if strings.TrimSpace(cfg.APIKey) != "" ||
		strings.TrimSpace(cfg.APIKey2) != "" ||
		strings.TrimSpace(cfg.IAMToken) != "" ||
		strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.Secret) != "" {
		return Config{}, fmt.Errorf("watsonx: literal secrets in configuration YAML are forbidden; supply via ConfigureRequest.Secrets")
	}

	cfg.Region = strings.TrimSpace(cfg.Region)
	if cfg.Region == "" {
		return Config{}, fmt.Errorf("watsonx: region is required")
	}

	cfg.ProjectID = strings.TrimSpace(cfg.ProjectID)
	cfg.SpaceID = strings.TrimSpace(cfg.SpaceID)
	if (cfg.ProjectID == "" && cfg.SpaceID == "") || (cfg.ProjectID != "" && cfg.SpaceID != "") {
		return Config{}, fmt.Errorf("watsonx: exactly one of project_id or space_id must be configured")
	}

	cfg.ModelID = strings.TrimSpace(cfg.ModelID)
	cfg.DeploymentID = strings.TrimSpace(cfg.DeploymentID)

	cfg.InferenceAPI = strings.TrimSpace(strings.ToLower(cfg.InferenceAPI))
	if cfg.InferenceAPI == "" {
		cfg.InferenceAPI = "chat"
	}
	if cfg.InferenceAPI != "chat" && cfg.InferenceAPI != "generation" {
		return Config{}, fmt.Errorf("watsonx: unsupported inference_api %q (expected \"chat\" or \"generation\")", cfg.InferenceAPI)
	}

	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.IAMOrigin = strings.TrimSpace(cfg.IAMOrigin)
	cfg.APIVersion = strings.TrimSpace(cfg.APIVersion)
	if cfg.APIVersion == "" {
		cfg.APIVersion = DefaultAPIVersion
	}
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	return cfg, nil
}

func (c Config) MLOrigin() string {
	if c.APIOrigin != "" {
		return strings.TrimRight(c.APIOrigin, "/")
	}
	return fmt.Sprintf("https://%s.ml.cloud.ibm.com", c.Region)
}

func (c Config) IAMOriginURL() string {
	if c.IAMOrigin != "" {
		return strings.TrimRight(c.IAMOrigin, "/")
	}
	return DefaultIAMOrigin
}

func (c Config) HTTPClient() (*http.Client, error) {
	timeout := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		d, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("watsonx: invalid http_timeout %q: %w", c.HTTPTimeout, err)
		}
		timeout = d
	}
	return &http.Client{Timeout: timeout}, nil
}
