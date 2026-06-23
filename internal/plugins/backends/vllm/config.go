package vllm

import (
	"net/http"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
)

const (
	defaultBaseURL          = "http://localhost:8000/v1"
	defaultDummyCredential  = "vllm"
	defaultCatalogURL       = "https://models.dev/api.json"
	defaultDiscoveryTimeout = 15 * time.Second
)

type DiscoveryConfig struct {
	Catalog    *bool
	CatalogURL string
	Timeout    time.Duration
}

type Config struct {
	BaseURL       string
	APIKey        string
	APIKeys       []string
	Credentials   []credpool.Credential
	HTTPClient    *http.Client
	SDKMaxRetries *int
	Discovery     DiscoveryConfig
}

func ApplyDefaults(cfg Config) Config {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Discovery.CatalogURL == "" {
		cfg.Discovery.CatalogURL = defaultCatalogURL
	}
	if cfg.Discovery.Timeout == 0 {
		cfg.Discovery.Timeout = defaultDiscoveryTimeout
	}
	return cfg
}

func DiscoveryCatalog(d DiscoveryConfig) bool {
	if d.Catalog == nil {
		return true
	}
	return *d.Catalog
}

func EffectiveCredentials(cfg Config) (apiKey string, apiKeys []string, credentials []credpool.Credential) {
	if cfg.APIKey == "" && len(cfg.APIKeys) == 0 && len(cfg.Credentials) == 0 {
		return defaultDummyCredential, nil, nil
	}
	return cfg.APIKey, cfg.APIKeys, cfg.Credentials
}
