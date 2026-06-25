package gemini

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/modeldiscover"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/geminigenerate"
)

// Config configures the Gemini generateContent backend connector (official genai SDK).
// BaseURL is the API origin (e.g. https://generativelanguage.googleapis.com) without a path suffix;
// the SDK appends /v1beta/models/... for Google AI backend.
//
// google.golang.org/genai does not apply the same automatic multi-attempt retries on
// generateContent/streamGenerateContent as openai-go and anthropic-sdk-go; credential tests
// can count httptest invocations without an SDKMaxRetries-style knob.
type Config struct {
	BaseURL string
	APIKey  string
	// APIKeys is the ordered credential list for this backend instance.
	// When non-empty, APIKey should match APIKeys[0] for SDK compatibility.
	APIKeys []string
	// Credentials is the structured credential list. When set, it takes precedence
	// over APIKey/APIKeys and preserves non-secret credential IDs for diagnostics.
	Credentials []credpool.Credential
	// HTTPClient is optional; when nil the SDK default is used.
	HTTPClient *http.Client
}

// New returns a runtime backend that invokes Gemini generateContent streaming via google.golang.org/genai.
func New(cfg Config) execbackend.Backend {
	return geminigenerate.NewBackend(geminigenerate.Config{
		BackendID:   ID,
		BaseURL:     cfg.BaseURL,
		APIKey:      cfg.APIKey,
		APIKeys:     cfg.APIKeys,
		Credentials: cfg.Credentials,
		HTTPClient:  cfg.HTTPClient,
		ModelInventory: modeldiscover.GeminiModelsProvider{
			BaseURL:    cfg.BaseURL,
			APIKey:     cfg.APIKey,
			APIKeys:    cfg.APIKeys,
			HTTPClient: cfg.HTTPClient,
		},
	})
}
