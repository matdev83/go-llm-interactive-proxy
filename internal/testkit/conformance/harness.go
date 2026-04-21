package conformance

import (
	"context"
	"math/rand"
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
)

// DefaultModel returns the model name wired into routing.AttemptCandidate for a bundled backend ID.
func DefaultModel(backendID string) string {
	return pluginreg.DefaultWireModel(backendID)
}

// RouteSelector builds a core routing selector primary for a single-backend executor.
func RouteSelector(backendID, model string) string {
	if model == "" {
		model = DefaultModel(backendID)
	}
	return backendID + ":" + model
}

// NewTestExecutor wires a single backend against refBackendURL (or error injection URL) for conformance cells.
func NewTestExecutor(tb testing.TB, backendID, upstreamBaseURL string, httpClient *http.Client) *runtime.Executor {
	tb.Helper()
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	be := BackendFor(tb, backendID, upstreamBaseURL, httpClient)
	return &runtime.Executor{
		Store:    b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{}),
		Bus:      hooks.New(hooks.Config{}),
		Rand:     rand.New(rand.NewSource(42)),
		Backends: map[string]runtime.Backend{backendID: be},
	}
}

// BackendFor returns the bundled runtime.Backend for upstreamBaseURL (httptest origin or /v1 layout per plugin).
func BackendFor(tb testing.TB, backendID, upstreamBaseURL string, httpClient *http.Client) runtime.Backend {
	tb.Helper()
	switch backendID {
	case openairesponses.ID:
		return openairesponses.New(openairesponses.Config{
			BaseURL:    upstreamBaseURL + "/v1",
			APIKey:     "sk-test",
			HTTPClient: httpClient,
		})
	case openailegacy.ID:
		return openailegacy.New(openailegacy.Config{
			BaseURL:    upstreamBaseURL + "/v1",
			APIKey:     "sk-test",
			HTTPClient: httpClient,
		})
	case anthropic.ID:
		return anthropic.New(anthropic.Config{
			BaseURL:    upstreamBaseURL,
			APIKey:     "sk-ant-test",
			HTTPClient: httpClient,
		})
	case gemini.ID:
		return gemini.New(gemini.Config{
			BaseURL:    upstreamBaseURL,
			APIKey:     "fake-key",
			HTTPClient: httpClient,
		})
	case bedrock.ID:
		return bedrock.New(bedrock.Config{
			Region:          "us-east-1",
			AccessKeyID:     "AKID",
			SecretAccessKey: "SECRET",
			BaseEndpoint:    upstreamBaseURL,
			DisableHTTPS:    true,
			HTTPClient:      httpClient,
		})
	case acp.ID:
		return acp.New(acp.Config{
			BaseURL:    upstreamBaseURL,
			HTTPClient: httpClient,
		})
	default:
		tb.Fatalf("unknown backend id %q", backendID)
		return runtime.Backend{}
	}
}

// GenAITestCtx is a shared background context for genai client construction in tests.
func GenAITestCtx() context.Context {
	return context.Background()
}
