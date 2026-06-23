package ollama

import (
	"net/http"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
)

const (
	defaultBaseURL          = "http://localhost:11434/v1"
	defaultDummyCredential  = "ollama"
	defaultResponsesAPI     = "auto"
	defaultCloudModelsURL   = "https://ollama.com/api/tags"
	defaultCatalogURL       = "https://models.dev/api.json"
	defaultDiscoveryTimeout = 15 * time.Second
)

type DiscoveryConfig struct {
	Enabled      *bool
	Local        *bool
	Cloud        *bool
	Catalog      *bool
	Capabilities *bool
	CloudURL     string
	CatalogURL   string
	Timeout      time.Duration
}

type Config struct {
	BaseURL       string
	APIKey        string
	APIKeys       []string
	Credentials   []credpool.Credential
	HTTPClient    *http.Client
	SDKMaxRetries *int
	ResponsesAPI  string
	Discovery     DiscoveryConfig
}

func ApplyDefaults(cfg Config) Config {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.ResponsesAPI == "" {
		cfg.ResponsesAPI = defaultResponsesAPI
	}
	if cfg.Discovery.CloudURL == "" {
		cfg.Discovery.CloudURL = defaultCloudModelsURL
	}
	if cfg.Discovery.CatalogURL == "" {
		cfg.Discovery.CatalogURL = defaultCatalogURL
	}
	if cfg.Discovery.Timeout == 0 {
		cfg.Discovery.Timeout = defaultDiscoveryTimeout
	}
	return cfg
}

func DiscoveryEnabled(d DiscoveryConfig) bool {
	return flagDefault(d.Enabled, true)
}

func DiscoveryLocal(d DiscoveryConfig) bool {
	return flagDefault(d.Local, true)
}

func DiscoveryCloud(d DiscoveryConfig) bool {
	return flagDefault(d.Cloud, true)
}

func DiscoveryCatalog(d DiscoveryConfig) bool {
	return flagDefault(d.Catalog, true)
}

func DiscoveryCapabilities(d DiscoveryConfig) bool {
	return flagDefault(d.Capabilities, true)
}

func flagDefault(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func EffectiveCredentials(cfg Config) (apiKey string, apiKeys []string, credentials []credpool.Credential) {
	if cfg.APIKey == "" && len(cfg.APIKeys) == 0 && len(cfg.Credentials) == 0 {
		return defaultDummyCredential, nil, nil
	}
	return cfg.APIKey, cfg.APIKeys, cfg.Credentials
}
