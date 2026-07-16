//go:build integration

package conformance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openrouter"
	refchat "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaichat"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// BackendFor constructs OpenRouter with explicit ModeProxy attribution so default
// product identity is exercised (intentional break from empty/legacy omission).
func TestOpenRouterBackendFor_defaultProxyAttributionHeaders(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var presentRef, presentTit, presentLegacy bool
	var referer, title string
	inner := refchat.NewHandler(refchat.Config{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		_, presentRef = r.Header["Http-Referer"]
		referer = r.Header.Get("HTTP-Referer")
		_, presentTit = r.Header["X-Openrouter-Title"]
		title = r.Header.Get("X-OpenRouter-Title")
		_, presentLegacy = r.Header["X-Title"]
		mu.Unlock()
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	be := BackendFor(t, openrouter.ID, srv.URL, srv.Client())
	es, err := be.Open(context.Background(), lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIChatCompletions,
			DeliveryMode:  lipapi.DeliveryModeNonStreaming,
			TransportMode: lipapi.TransportModeNonStreaming,
		},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: DefaultModel(openrouter.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lipapi.Collect(context.Background(), es)

	mu.Lock()
	defer mu.Unlock()
	if presentLegacy {
		t.Fatal("must not emit legacy X-Title")
	}
	if !presentRef || referer != "https://github.com/matdev83/go-llm-interactive-proxy" {
		t.Fatalf("HTTP-Referer present=%v value=%q", presentRef, referer)
	}
	if !presentTit || title != "go-llm-interactive-proxy" {
		t.Fatalf("X-OpenRouter-Title present=%v value=%q", presentTit, title)
	}
}
