package openrouter_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openrouter"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNew_staticRefererAndTitleInjectedViaOpendaifamily(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedReferer, capturedTitle string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedReferer = r.Header.Get("HTTP-Referer")
		capturedTitle = r.Header.Get("X-Title")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"or-test","object":"chat.completion","created":1,"model":"openai/gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	be := openrouter.New(openrouter.Config{
		BaseURL:       srv.URL,
		APIKey:        "sk-test",
		StaticReferer: "https://myapp.example",
		StaticTitle:   "MyApp",
	})

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
		}},
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIChatCompletions,
			DeliveryMode:  lipapi.DeliveryModeNonStreaming,
			TransportMode: lipapi.TransportModeNonStreaming,
		},
	}

	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "openai/gpt-4o-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = es.Close()

	mu.Lock()
	defer mu.Unlock()
	if capturedReferer != "https://myapp.example" {
		t.Fatalf("HTTP-Referer = %q", capturedReferer)
	}
	if capturedTitle != "MyApp" {
		t.Fatalf("X-Title = %q", capturedTitle)
	}
}
