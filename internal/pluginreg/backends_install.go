package pluginreg

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type openAIStyleYAML struct {
	BaseURL     string                 `yaml:"base_url"`
	APIKey      string                 `yaml:"api_key"`
	APIKeys     []string               `yaml:"api_keys"`
	Credentials []hostedCredentialYAML `yaml:"credentials"`
	Models      modelInventoryYAML     `yaml:"models"`
}

func resolveUpstreamHTTP(upstream *http.Client) *http.Client {
	if upstream != nil {
		return upstream
	}
	return httpclient.Standard()
}

func applyConfiguredModelInventory(be execbackend.Backend, y modelInventoryYAML) (execbackend.Backend, error) {
	provider, ok, err := configuredModelInventory(y)
	if err != nil {
		return execbackend.Backend{}, err
	}
	if ok {
		be.ModelInventory = provider
	}
	return be, nil
}

func configuredModelInventory(y modelInventoryYAML) (modelinventory.Provider, bool, error) {
	rows, source, ok, err := modelInventoryRows(y, false)
	if err != nil || !ok {
		return nil, false, err
	}
	return staticModelInventory(source, rows)
}
