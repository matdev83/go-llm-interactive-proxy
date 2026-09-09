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
	DefaultPublisher   = "google"
)

type Config struct {
	Project     string `yaml:"project"`
	Location    string `yaml:"location"`
	Publisher   string `yaml:"publisher"`
	Model       string `yaml:"model"`
	APIOrigin   string `yaml:"api_origin"`
	HTTPTimeout string `yaml:"http_timeout"`

	// Secrets in YAML are strictly forbidden and rejected if present.
	APIKey             string `yaml:"api_key"`
	APIToken           string `yaml:"api_token"`
	Token              string `yaml:"token"`
	BearerToken        string `yaml:"bearer_token"`
	ServiceAccountJSON string `yaml:"service_account_json"`
	PrivateKey         string `yaml:"private_key"`
	ClientSecret       string `yaml:"client_secret"`
	Secret             string `yaml:"secret"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("vertex: config: %w", err)
		}
	}

	// Reject literal secrets in YAML
	if strings.TrimSpace(cfg.APIKey) != "" ||
		strings.TrimSpace(cfg.APIToken) != "" ||
		strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.BearerToken) != "" ||
		strings.TrimSpace(cfg.ServiceAccountJSON) != "" ||
		strings.TrimSpace(cfg.PrivateKey) != "" ||
		strings.TrimSpace(cfg.ClientSecret) != "" ||
		strings.TrimSpace(cfg.Secret) != "" {
		return Config{}, fmt.Errorf("vertex: literal secrets in configuration YAML are forbidden; supply via ConfigureRequest.Secrets")
	}

	cfg.Project = strings.TrimSpace(cfg.Project)
	cfg.Location = strings.TrimSpace(cfg.Location)
	cfg.Publisher = strings.TrimSpace(cfg.Publisher)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	if cfg.Project == "" {
		return Config{}, fmt.Errorf("vertex: project is required")
	}
	if strings.Contains(cfg.Project, "://") || strings.Contains(cfg.Project, "/") {
		return Config{}, fmt.Errorf("vertex: invalid project name %q", cfg.Project)
	}
	if cfg.Location == "" {
		return Config{}, fmt.Errorf("vertex: location is required")
	}
	if cfg.Publisher == "" {
		cfg.Publisher = DefaultPublisher
	}

	return cfg, nil
}

func (c Config) BaseOrigin() string {
	if c.APIOrigin != "" {
		return strings.TrimRight(strings.TrimSpace(c.APIOrigin), "/")
	}
	loc := strings.ToLower(strings.TrimSpace(c.Location))
	if loc == "global" {
		return "https://aiplatform.googleapis.com"
	}
	return fmt.Sprintf("https://%s-aiplatform.googleapis.com", loc)
}

func (c Config) ModelEndpoint(model string, stream bool) string {
	pub := strings.TrimSpace(c.Publisher)
	if pub == "" {
		pub = DefaultPublisher
	}
	origin := c.BaseOrigin()
	action := ":generateContent"
	if stream {
		action = ":streamGenerateContent?alt=sse"
	}
	return fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/%s/models/%s%s",
		origin, c.Project, c.Location, pub, model, action)
}

func (c Config) InventoryEndpoint() string {
	pub := strings.TrimSpace(c.Publisher)
	if pub == "" {
		pub = DefaultPublisher
	}
	origin := c.BaseOrigin()
	return fmt.Sprintf("%s/v1beta1/publishers/%s/models", origin, pub)
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("vertex: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
