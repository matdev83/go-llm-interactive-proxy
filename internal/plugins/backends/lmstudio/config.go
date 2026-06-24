package lmstudio

import (
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaifamily"
)

const (
	defaultBaseURL          = "http://localhost:1234/v1"
	defaultDummyCredential  = "lmstudio"
	defaultCatalogURL       = "https://models.dev/api.json"
	defaultDiscoveryTimeout = 15 * time.Second
)

type (
	Config          = openaifamily.Config
	DiscoveryConfig = openaifamily.DiscoveryConfig
)

var profile = openaifamily.Profile{
	ID:                      ID,
	DefaultBaseURL:          defaultBaseURL,
	DefaultDummyCredential:  defaultDummyCredential,
	DefaultCatalogURL:       defaultCatalogURL,
	DefaultDiscoveryTimeout: defaultDiscoveryTimeout,
	Transport:               openaifamily.TransportChatOnly,
	ModelResolution:         openaifamily.ModelResolutionStripBackendPrefix,
	Inventory:               openaifamily.InventoryCatalogAware,
}

func ApplyDefaults(cfg Config) Config {
	return openaifamily.ApplyDefaults(profile, cfg)
}

func DiscoveryCatalog(d DiscoveryConfig) bool {
	return openaifamily.DiscoveryCatalog(d)
}

func EffectiveCredentials(cfg Config) (apiKey string, apiKeys []string, credentials []credpool.Credential) {
	return openaifamily.EffectiveCredentials(profile, cfg)
}

func New(cfg Config) execbackend.Backend {
	return openaifamily.New(profile, cfg)
}
