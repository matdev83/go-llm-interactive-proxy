package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultHTTPTimeout         = 60 * time.Second
	InferenceContractHFTextGen = "hf-text-generation"
)

type Config struct {
	Region                 string `yaml:"region"`
	EndpointName           string `yaml:"endpoint_name"`
	InferenceContract      string `yaml:"inference_contract"`
	APIOrigin              string `yaml:"api_origin"`
	TargetModel            string `yaml:"target_model"`
	InferenceComponentName string `yaml:"inference_component_name"`
	AWSProfile             string `yaml:"aws_profile"`
	HTTPTimeout            string `yaml:"http_timeout"`

	// Secrets in YAML are strictly forbidden and rejected if present.
	AWSAccessKeyID     string `yaml:"aws_access_key_id"`
	AWSSecretAccessKey string `yaml:"aws_secret_access_key"`
	AWSSessionToken    string `yaml:"aws_session_token"`
	AccessKey          string `yaml:"access_key"`
	SecretKey          string `yaml:"secret_key"`
	APIKey             string `yaml:"api_key"`
	Token              string `yaml:"token"`
	Secret             string `yaml:"secret"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("sagemaker: config: %w", err)
		}
	}

	// Reject literal secrets in YAML
	if strings.TrimSpace(cfg.AWSAccessKeyID) != "" ||
		strings.TrimSpace(cfg.AWSSecretAccessKey) != "" ||
		strings.TrimSpace(cfg.AWSSessionToken) != "" ||
		strings.TrimSpace(cfg.AccessKey) != "" ||
		strings.TrimSpace(cfg.SecretKey) != "" ||
		strings.TrimSpace(cfg.APIKey) != "" ||
		strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.Secret) != "" {
		return Config{}, fmt.Errorf("sagemaker: literal secrets in configuration YAML are forbidden; supply via ConfigureRequest.Secrets")
	}

	cfg.Region = strings.TrimSpace(cfg.Region)
	cfg.EndpointName = strings.TrimSpace(cfg.EndpointName)
	cfg.InferenceContract = strings.TrimSpace(cfg.InferenceContract)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.TargetModel = strings.TrimSpace(cfg.TargetModel)
	cfg.InferenceComponentName = strings.TrimSpace(cfg.InferenceComponentName)
	cfg.AWSProfile = strings.TrimSpace(cfg.AWSProfile)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	if cfg.Region == "" {
		return Config{}, fmt.Errorf("sagemaker: region is required")
	}
	if cfg.EndpointName == "" {
		return Config{}, fmt.Errorf("sagemaker: endpoint_name is required")
	}
	if cfg.InferenceContract == "" {
		return Config{}, fmt.Errorf("sagemaker: inference_contract is required")
	}
	if cfg.InferenceContract != InferenceContractHFTextGen {
		return Config{}, fmt.Errorf("sagemaker: unsupported inference_contract %q; only %q is supported in v1", cfg.InferenceContract, InferenceContractHFTextGen)
	}

	return cfg, nil
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("sagemaker: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
