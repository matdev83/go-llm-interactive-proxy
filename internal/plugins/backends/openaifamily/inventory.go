package openaifamily

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/modeldiscover"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type InventoryPolicy int

const (
	InventoryCatalogAware InventoryPolicy = iota
	InventoryOpenAICompatible
)

func inventoryProvider(profile Profile, cfg Config, apiKey string, apiKeys []string, credentials []credpool.Credential) modelinventory.Provider {
	switch profile.Inventory {
	case InventoryOpenAICompatible:
		return modeldiscover.OpenAICompatibleModelsProvider{
			BaseURL:           cfg.BaseURL,
			APIKey:            apiKey,
			APIKeys:           apiKeys,
			Credentials:       credpool.Secrets(credentials),
			HTTPClient:        cfg.HTTPClient,
			CanonicalPrefix:   profile.ID,
			PreserveVendorIDs: true,
		}
	default:
		catalogEnabled := DiscoveryCatalog(cfg.Discovery)
		return modeldiscover.CatalogAwareOpenAICompatibleModelsProvider{
			BaseURL:           cfg.BaseURL,
			APIKey:            apiKey,
			APIKeys:           apiKeys,
			Credentials:       credpool.Secrets(credentials),
			HTTPClient:        cfg.HTTPClient,
			CanonicalPrefix:   profile.ID,
			PreserveVendorIDs: true,
			Catalog: modeldiscover.CatalogConfig{
				Enabled: &catalogEnabled,
				URL:     cfg.Discovery.CatalogURL,
				Timeout: cfg.Discovery.Timeout,
			},
		}
	}
}
