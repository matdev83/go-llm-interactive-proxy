package pluginreg

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/ollama"
	"gopkg.in/yaml.v3"
)

type ollamaDiscoveryYAML struct {
	Enabled        *bool  `yaml:"enabled"`
	LocalModels    *bool  `yaml:"local_models"`
	CloudModels    *bool  `yaml:"cloud_models"`
	Catalog        *bool  `yaml:"catalog"`
	Capabilities   *bool  `yaml:"capabilities"`
	CloudModelsURL string `yaml:"cloud_models_url"`
	CatalogURL     string `yaml:"catalog_url"`
	Timeout        string `yaml:"timeout"`
}

type ollamaBackendYAML struct {
	BaseURL      string                 `yaml:"base_url"`
	APIKey       string                 `yaml:"api_key"`
	APIKeys      []string               `yaml:"api_keys"`
	Credentials  []hostedCredentialYAML `yaml:"credentials"`
	ResponsesAPI string                 `yaml:"responses_api"`
	Discovery    ollamaDiscoveryYAML    `yaml:"discovery"`
	Models       modelInventoryYAML     `yaml:"models"`
}

func backendOllama(n yaml.Node, upstream *http.Client, _ UpstreamAPIKeys) (execbackend.Backend, error) {
	cfg, models, err := parseOllamaBackendConfig(n, upstream)
	if err != nil {
		return execbackend.Backend{}, err
	}
	return applyConfiguredModelInventory(ollama.New(cfg), models)
}

func backendOllamaCloud(n yaml.Node, upstream *http.Client, _ UpstreamAPIKeys) (execbackend.Backend, error) {
	cfg, models, err := parseOllamaBackendConfig(n, upstream)
	if err != nil {
		return execbackend.Backend{}, err
	}
	return applyConfiguredModelInventory(ollama.NewCloud(cfg), models)
}

func parseOllamaBackendConfig(n yaml.Node, upstream *http.Client) (ollama.Config, modelInventoryYAML, error) {
	var y ollamaBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return ollama.Config{}, modelInventoryYAML{}, fmt.Errorf("ollama backend config: %w", err)
	}
	ek, primaryKey := firstAPIKey(y.APIKey, y.APIKeys, y.Credentials, nil)
	discovery := ollama.DiscoveryConfig{
		Enabled:      y.Discovery.Enabled,
		Local:        y.Discovery.LocalModels,
		Cloud:        y.Discovery.CloudModels,
		Catalog:      y.Discovery.Catalog,
		Capabilities: y.Discovery.Capabilities,
		CloudURL:     strings.TrimSpace(y.Discovery.CloudModelsURL),
		CatalogURL:   strings.TrimSpace(y.Discovery.CatalogURL),
	}
	if timeout := strings.TrimSpace(y.Discovery.Timeout); timeout != "" {
		d, err := time.ParseDuration(timeout)
		if err != nil {
			return ollama.Config{}, modelInventoryYAML{}, fmt.Errorf("ollama discovery timeout: %w", err)
		}
		discovery.Timeout = d
	}
	cfg := ollama.Config{
		BaseURL:      strings.TrimSpace(y.BaseURL),
		APIKey:       primaryKey,
		APIKeys:      ek,
		Credentials:  hostedCredentials(y.Credentials),
		HTTPClient:   resolveUpstreamHTTP(upstream),
		ResponsesAPI: strings.TrimSpace(y.ResponsesAPI),
		Discovery:    discovery,
	}
	return cfg, y.Models, nil
}
