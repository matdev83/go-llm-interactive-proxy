package pluginreg

import (
	"cmp"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openrouter"
	"gopkg.in/yaml.v3"
)

type openRouterBackendYAML struct {
	BaseURL       string                 `yaml:"base_url"`
	APIKey        string                 `yaml:"api_key"`
	APIKeys       []string               `yaml:"api_keys"`
	Credentials   []hostedCredentialYAML `yaml:"credentials"`
	StaticReferer string                 `yaml:"static_referer"`
	StaticTitle   string                 `yaml:"static_title"`
	Models        modelInventoryYAML     `yaml:"models"`
}

func backendOpenRouter(n yaml.Node, upstream *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
	var y openRouterBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("openrouter backend config: %w", err)
	}
	base := cmp.Or(strings.TrimSpace(y.BaseURL), "https://openrouter.ai/api/v1")
	ek := inventoryAPIKeys(y.APIKey, y.APIKeys, y.Credentials, keys.OpenRouter)
	cfg := openrouter.Config{
		BaseURL:       base,
		APIKeys:       ek,
		Credentials:   hostedCredentials(y.Credentials),
		HTTPClient:    resolveUpstreamHTTP(upstream),
		StaticReferer: strings.TrimSpace(y.StaticReferer),
		StaticTitle:   strings.TrimSpace(y.StaticTitle),
	}
	if len(ek) > 0 {
		cfg.APIKey = ek[0]
	}
	return applyConfiguredModelInventory(openrouter.New(cfg), y.Models)
}
