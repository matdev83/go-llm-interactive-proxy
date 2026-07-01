package openailegacy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	front "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// TestIntegration_openaiCodexURIReasoningEffortOverridesBody proves that a reasoning_effort
// URI param on the model selector OVERRIDES the Chat Completions body's reasoning_effort
// field. URI params are explicit routing directives and take precedence over per-request
// body settings.
func TestIntegration_openaiCodexURIReasoningEffortOverridesBody(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var captured lipapi.Call
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(42),
		Backends: map[string]execbackend.Backend{
			"openai-codex": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityReasoning, lipapi.CapabilityTools),
				Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					captured = call
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventTextDelta, Delta: "ok"},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	testkit.WireConformanceExecutorSecureSession(t, ex)

	h := &front.Handler{Exec: ex, DefaultRouteSelector: "openai-codex:gpt-5.5", RoutePrefixes: routeselect.NewPrefixSet([]string{"openai-codex"})}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"openai-codex:openai/gpt-5.4-mini?reasoning_effort=xhigh","reasoning_effort":"medium","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := testkit.IntegrationHTTPClient(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}

	if captured.Options.ReasoningEffort != "xhigh" {
		t.Fatalf("call.Options.ReasoningEffort %q, want %q (URI param must override body field)", captured.Options.ReasoningEffort, "xhigh")
	}
}
