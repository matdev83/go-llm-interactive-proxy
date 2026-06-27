package openrouter

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaifamily"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/option"
)

// Config configures the OpenRouter backend connector.
// BaseURL should be https://openrouter.ai/api/v1 (no trailing slash).
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
	// StaticHeaders are always sent. Per-request headers from Call.Extensions take precedence.
	StaticReferer string
	StaticTitle   string
}

var profile = openaifamily.Profile{
	ID:              ID,
	Transport:       openaifamily.TransportChatAndResponses,
	ModelResolution: openaifamily.ModelResolutionDirect,
	Inventory:       openaifamily.InventoryOpenAICompatible,
}

// New returns a runtime backend that invokes OpenRouter via the openai-go SDK.
func New(cfg Config) execbackend.Backend {
	profile := profile
	profile.ClientOptions = func(call lipapi.Call, cand routing.AttemptCandidate) []option.RequestOption {
		return buildRequestOptions(call, cand, cfg)
	}
	return openaifamily.New(profile, openaifamily.Config{
		BaseURL:       cfg.BaseURL,
		APIKey:        cfg.APIKey,
		APIKeys:       cfg.APIKeys,
		Credentials:   cfg.Credentials,
		HTTPClient:    cfg.HTTPClient,
		SDKMaxRetries: cfg.SDKMaxRetries,
	})
}
