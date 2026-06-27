package huggingface

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaifamily"
)

type Config struct {
	BaseURL       string
	APIKey        string
	APIKeys       []string
	Credentials   []credpool.Credential
	HTTPClient    *http.Client
	SDKMaxRetries *int
}

var profile = openaifamily.Profile{
	ID:              ID,
	Transport:       openaifamily.TransportChatOnly,
	ModelResolution: openaifamily.ModelResolutionDirect,
	Inventory:       openaifamily.InventoryOpenAICompatible,
}

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
