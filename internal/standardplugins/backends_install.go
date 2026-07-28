package standardplugins

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
	// SDKMaxRetries optionally overrides the SDK-internal MaxRetries.
	SDKMaxRetries *int               `yaml:"sdk_max_retries"`
	Models        modelInventoryYAML `yaml:"models"`
}

// sdkMaxRetriesOrDefault returns the operator-configured SDK MaxRetries, or the
// standard-distribution default of 0: retry policy above the HTTP round trip lives
// in the credential-rotation loops and core failover, not in SDK-internal retries.
func sdkMaxRetriesOrDefault(v *int) *int {
	if v != nil {
		return v
	}
	return new(int)
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
