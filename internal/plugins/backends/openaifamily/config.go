package openaifamily

import (
	"net/http"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/option"
)

const (
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

type Profile struct {
	ID                      string
	DefaultBaseURL          string
	DefaultDummyCredential  string
	DefaultCatalogURL       string
	DefaultDiscoveryTimeout time.Duration
	Transport               TransportPolicy
	ModelResolution         ModelResolutionPolicy
	Inventory               InventoryPolicy
	ClientOptions           func(lipapi.Call) []option.RequestOption
	RequestOptions          func(lipapi.Call) []option.RequestOption
}

func ApplyDefaults(profile Profile, cfg Config) Config {
	if cfg.BaseURL == "" {
		cfg.BaseURL = profile.DefaultBaseURL
	}
	catalogURL := profile.DefaultCatalogURL
	if catalogURL == "" {
		catalogURL = defaultCatalogURL
	}
	if cfg.Discovery.CatalogURL == "" {
		cfg.Discovery.CatalogURL = catalogURL
	}
	timeout := profile.DefaultDiscoveryTimeout
	if timeout == 0 {
		timeout = defaultDiscoveryTimeout
	}
	if cfg.Discovery.Timeout == 0 {
		cfg.Discovery.Timeout = timeout
	}
	return cfg
}

func DiscoveryCatalog(d DiscoveryConfig) bool {
	if d.Catalog == nil {
		return true
	}
	return *d.Catalog
}

func EffectiveCredentials(profile Profile, cfg Config) (apiKey string, apiKeys []string, credentials []credpool.Credential) {
	if cfg.APIKey == "" && len(cfg.APIKeys) == 0 && len(cfg.Credentials) == 0 {
		return profile.DefaultDummyCredential, nil, nil
	}
	return cfg.APIKey, cfg.APIKeys, cfg.Credentials
}
