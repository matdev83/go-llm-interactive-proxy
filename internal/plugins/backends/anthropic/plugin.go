package anthropic

import (
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/modeldiscover"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/anthropicmessages"
)

// Config configures the Anthropic Messages API backend connector (official SDK).
// BaseURL is the API origin only (e.g. https://api.anthropic.com), without a /v1 suffix;
// the SDK posts to /v1/messages relative to it.
type Config struct {
	BaseURL string
	APIKey  string
	// BackendPrefix overrides the backend/model-inventory prefix. Empty uses the bundled Anthropic ID.
	BackendPrefix string
	// APIKeys is the ordered credential list for this backend instance.
	// When non-empty, APIKey should match APIKeys[0] for SDK compatibility.
	APIKeys []string
	// Credentials is the structured credential list. When set, it takes precedence
	// over APIKey/APIKeys and preserves non-secret credential IDs for diagnostics.
	Credentials []credpool.Credential
	// HTTPClient is optional; when nil the SDK default is used.
	HTTPClient *http.Client
	// SDKMaxRetries optionally sets the official SDK MaxRetries (nil = SDK default).
	// Integration tests that assert a single upstream attempt on 429/401 should use a pointer to 0.
	SDKMaxRetries *int
}

const anthropicRateLimitFallback = 60 * time.Second

// New returns a runtime backend that invokes the Anthropic Messages API using anthropic-sdk-go.
func New(cfg Config) execbackend.Backend {
	id := strings.TrimSpace(cfg.BackendPrefix)
	if id == "" {
		id = ID
	}
	return anthropicmessages.NewBackend(anthropicmessages.Config{
		BackendID:         id,
		BaseURL:           cfg.BaseURL,
		APIKey:            cfg.APIKey,
		APIKeys:           cfg.APIKeys,
		Credentials:       cfg.Credentials,
		HTTPClient:        cfg.HTTPClient,
		SDKMaxRetries:     cfg.SDKMaxRetries,
		RateLimitFallback: anthropicRateLimitFallback,
		ProviderCounter:   NewTokenCounter(cfg),
		ModelInventory: modeldiscover.AnthropicModelsProvider{
			BaseURL:         cfg.BaseURL,
			APIKey:          cfg.APIKey,
			APIKeys:         cfg.APIKeys,
			HTTPClient:      cfg.HTTPClient,
			CanonicalPrefix: id,
		},
	})
}
