package openrouter

import (
	"net/http"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/modeldiscover"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
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

const rateLimitFallback = 60 * time.Second

// New returns a runtime backend that invokes OpenRouter via the openai-go SDK.
func New(cfg Config) execbackend.Backend {
	return openaicompat.NewBackend(openaicompat.BackendSpec{
		ID:                ID,
		BaseURL:           cfg.BaseURL,
		APIKey:            cfg.APIKey,
		APIKeys:           cfg.APIKeys,
		Credentials:       cfg.Credentials,
		HTTPClient:        cfg.HTTPClient,
		SDKMaxRetries:     cfg.SDKMaxRetries,
		RateLimitFallback: rateLimitFallback,
		ClientOptions: func(call lipapi.Call) []option.RequestOption {
			return buildRequestOptions(call, cfg)
		},
		ResolveModel: resolveModel,
		Inventory: modeldiscover.OpenAICompatibleModelsProvider{
			BaseURL:           cfg.BaseURL,
			APIKey:            cfg.APIKey,
			APIKeys:           cfg.APIKeys,
			HTTPClient:        cfg.HTTPClient,
			CanonicalPrefix:   "openrouter",
			PreserveVendorIDs: true,
		},
		ResolveFlavor: func(call lipapi.Call) openaicompat.Flavor {
			if resolveFlavor(call) == openrouterwire.FlavorResponses {
				return openaicompat.FlavorResponses
			}
			return openaicompat.FlavorChat
		},
	})
}
