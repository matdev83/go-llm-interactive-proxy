package pluginreg

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaifamily"
)

type openAIFamilyDiscoveryYAML struct {
	Catalog    *bool  `yaml:"catalog"`
	CatalogURL string `yaml:"catalog_url"`
	Timeout    string `yaml:"timeout"`
}

type openAIFamilyBackendYAML struct {
	BaseURL     string                    `yaml:"base_url"`
	APIKey      string                    `yaml:"api_key"`
	APIKeys     []string                  `yaml:"api_keys"`
	Credentials []hostedCredentialYAML    `yaml:"credentials"`
	Discovery   openAIFamilyDiscoveryYAML `yaml:"discovery"`
	Models      modelInventoryYAML        `yaml:"models"`
}

func decodeOpenAIFamilyDiscovery(backendID string, d openAIFamilyDiscoveryYAML) (openaifamily.DiscoveryConfig, error) {
	discovery := openaifamily.DiscoveryConfig{
		Catalog:    d.Catalog,
		CatalogURL: strings.TrimSpace(d.CatalogURL),
	}
	if timeout := strings.TrimSpace(d.Timeout); timeout != "" {
		parsed, err := time.ParseDuration(timeout)
		if err != nil {
			return openaifamily.DiscoveryConfig{}, fmt.Errorf("%s discovery timeout: %w", backendID, err)
		}
		discovery.Timeout = parsed
	}
	return discovery, nil
}

func openAIFamilyConfigFromYAML(backendID string, y openAIFamilyBackendYAML, upstream *http.Client) (openaifamily.Config, error) {
	ek := inventoryAPIKeys(y.APIKey, y.APIKeys, y.Credentials, nil)
	discovery, err := decodeOpenAIFamilyDiscovery(backendID, y.Discovery)
	if err != nil {
		return openaifamily.Config{}, err
	}
	cfg := openaifamily.Config{
		BaseURL:     strings.TrimSpace(y.BaseURL),
		APIKeys:     ek,
		Credentials: hostedCredentials(y.Credentials),
		HTTPClient:  resolveUpstreamHTTP(upstream),
		Discovery:   discovery,
	}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return cfg, nil
}
