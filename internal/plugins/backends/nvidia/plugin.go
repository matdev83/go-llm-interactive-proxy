package nvidia

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaifamily"
)

// Config configures the NVIDIA NIM backend connector.
// BaseURL should be https://integrate.api.nvidia.com/v1 (no trailing slash).
type Config struct {
	BaseURL string
	APIKey  string
	APIKeys []string
	// Credentials is the structured credential list. When set, it takes precedence
	// over APIKey/APIKeys.
	Credentials []credpool.Credential
	HTTPClient  *http.Client
	// SDKMaxRetries optionally sets the official SDK MaxRetries (nil = SDK default).
	SDKMaxRetries *int
}

var profile = openaifamily.Profile{
	ID:              ID,
	Transport:       openaifamily.TransportChatAndResponses,
	ModelResolution: openaifamily.ModelResolutionDirect,
	Inventory:       openaifamily.InventoryOpenAICompatible,
	RequestOptions:  requestOptions,
}

// New returns a runtime backend that invokes NVIDIA NIM via the openai-go SDK.
func New(cfg Config) execbackend.Backend {
	return openaifamily.New(profile, openaifamily.Config{
		BaseURL:       cfg.BaseURL,
		APIKey:        cfg.APIKey,
		APIKeys:       cfg.APIKeys,
		Credentials:   cfg.Credentials,
		HTTPClient:    cfg.HTTPClient,
		SDKMaxRetries: cfg.SDKMaxRetries,
	})
}
