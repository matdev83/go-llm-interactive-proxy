package conformance

import (
	"context"
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
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
	httpClient = testkit.IntegrationHTTPClient(httpClient)
	be := BackendFor(tb, backendID, upstreamBaseURL, httpClient)
	return newExecutorWithBackend(tb, backendID, be)
}

// NewTestExecutorDualCredential wires hosted OpenAI, Anthropic, and Gemini backends with two ordered
// API keys so credential pools are populated. Bedrock and ACP use the same construction as
// [NewTestExecutor] (no multi-key pool in this harness).
func NewTestExecutorDualCredential(tb testing.TB, backendID, upstreamBaseURL string, httpClient *http.Client) *runtime.Executor {
	tb.Helper()
	httpClient = testkit.IntegrationHTTPClient(httpClient)
	be := BackendForDualCredential(tb, backendID, upstreamBaseURL, httpClient)
	return newExecutorWithBackend(tb, backendID, be)
}

func newExecutorWithBackend(tb testing.TB, backendID string, be execbackend.Backend) *runtime.Executor {
	tb.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		tb.Fatal(err)
	}
	ex := &runtime.Executor{
		Store:    st,
		Bus:      hooks.New(hooks.Config{}),
		Rand:     routing.NewSeededRng(42),
		Backends: map[string]execbackend.Backend{backendID: be},
	}
	testkit.WireConformanceExecutorSecureSession(tb, ex)
	return ex
}

// BackendFor returns the bundled [execbackend.Backend] for upstreamBaseURL (httptest origin or /v1 layout per plugin).
func BackendFor(tb testing.TB, backendID, upstreamBaseURL string, httpClient *http.Client) execbackend.Backend {
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
			APIKey:     testkit.SyntheticAnthropicAPIKey,
			HTTPClient: httpClient,
		})
	case gemini.ID:
		return gemini.New(gemini.Config{
			BaseURL:    upstreamBaseURL,
			APIKey:     "fake-key",
			HTTPClient: httpClient,
		})
	case bedrock.ID:
		ctx, cancel := context.WithTimeout(context.Background(), bedrock.DefaultLoadConfigTimeout)
		defer cancel()
		return bedrock.NewWithContext(ctx, bedrock.Config{
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
		return execbackend.Backend{}
	}
}

// BackendForDualCredential is like [BackendFor] but supplies a second synthetic key for hosted
// providers that support credential pools. Reference backends accept any non-empty key material
// used by conformance clients (sk-test, synthetic Anthropic key, fake Gemini key).
func BackendForDualCredential(tb testing.TB, backendID, upstreamBaseURL string, httpClient *http.Client) execbackend.Backend {
	tb.Helper()
	switch backendID {
	case openairesponses.ID:
		return openairesponses.New(openairesponses.Config{
			BaseURL:    upstreamBaseURL + "/v1",
			APIKey:     "sk-test",
			APIKeys:    []string{"sk-test", "sk-test-pool2"},
			HTTPClient: httpClient,
		})
	case openailegacy.ID:
		return openailegacy.New(openailegacy.Config{
			BaseURL:    upstreamBaseURL + "/v1",
			APIKey:     "sk-test",
			APIKeys:    []string{"sk-test", "sk-test-pool2"},
			HTTPClient: httpClient,
		})
	case anthropic.ID:
		k := testkit.SyntheticAnthropicAPIKey
		return anthropic.New(anthropic.Config{
			BaseURL:    upstreamBaseURL,
			APIKey:     k,
			APIKeys:    []string{k, k + "-pool2"},
			HTTPClient: httpClient,
		})
	case gemini.ID:
		return gemini.New(gemini.Config{
			BaseURL:    upstreamBaseURL,
			APIKey:     "fake-key",
			APIKeys:    []string{"fake-key", "fake-key-pool2"},
			HTTPClient: httpClient,
		})
	case bedrock.ID, acp.ID:
		return BackendFor(tb, backendID, upstreamBaseURL, httpClient)
	default:
		tb.Fatalf("unknown backend id %q", backendID)
		return execbackend.Backend{}
	}
}

// GenAITestCtx is a shared background context for genai client construction in tests.
func GenAITestCtx() context.Context {
	return context.Background()
}
