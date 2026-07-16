package openrouter_test

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/identity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openrouter"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNew_staticRefererAndTitleInjectedViaOpendaifamily(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedReferer, capturedTitle string
	var sawLegacyTitle bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedReferer = r.Header.Get("HTTP-Referer")
		capturedTitle = r.Header.Get("X-OpenRouter-Title")
		_, sawLegacyTitle = r.Header["X-Title"]
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"or-test","object":"chat.completion","created":1,"model":"openai/gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	be := openrouter.New(openrouter.Config{
		BaseURL:        srv.URL,
		APIKey:         "sk-test",
		LegacyAppURL:   true,
		LegacyAppTitle: true,
		StaticReferer:  "https://myapp.example",
		StaticTitle:    "MyApp",
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
		t.Fatalf("X-OpenRouter-Title = %q", capturedTitle)
	}
	if sawLegacyTitle {
		t.Fatal("must not emit legacy X-Title")
	}
}

func TestAttribution_wireHeadersBothFlavors(t *testing.T) {
	t.Parallel()

	flavors := []struct {
		name string
		ext  map[string]json.RawMessage
		op   lipapi.Operation
	}{
		{
			name: "chat",
			op:   lipapi.OperationOpenAIChatCompletions,
			ext: map[string]json.RawMessage{
				openrouterwire.ExtUpstreamFlavor: json.RawMessage(`"chat"`),
			},
		},
		{
			name: "responses",
			op:   lipapi.OperationOpenAIResponses,
			ext: map[string]json.RawMessage{
				openrouterwire.ExtUpstreamFlavor: json.RawMessage(`"responses"`),
			},
		},
	}

	for _, fl := range flavors {
		t.Run(fl.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var referer, title string
			var sawLegacy bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				referer = r.Header.Get("HTTP-Referer")
				title = r.Header.Get("X-OpenRouter-Title")
				_, sawLegacy = r.Header["X-Title"]
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if fl.name == "responses" {
					_, _ = w.Write([]byte(`{"id":"resp","object":"response","created_at":1,"status":"completed","model":"openai/gpt-4o-mini","output":[{"type":"message","id":"m","role":"assistant","content":[{"type":"output_text","text":"ok"}],"status":"completed"}]}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"or-test","object":"chat.completion","created":1,"model":"openai/gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			}))
			t.Cleanup(srv.Close)

			ext := make(map[string]json.RawMessage, len(fl.ext))
			maps.Copy(ext, fl.ext)
			ext[openrouterwire.ExtHTTPReferer] = json.RawMessage(`"https://flavor.example/"`)
			ext[openrouterwire.ExtTitle] = json.RawMessage(`"FlavorTitle"`)

			be := openrouter.New(openrouter.Config{
				BaseURL:  srv.URL,
				APIKey:   "sk-test",
				AppURL:   identity.FieldPolicy{Mode: identity.ModePassthrough},
				AppTitle: identity.FieldPolicy{Mode: identity.ModePassthrough},
			})
			call := lipapi.Call{
				Messages: []lipapi.Message{{
					Role:  lipapi.RoleUser,
					Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
				}},
				Extensions: ext,
				Invocation: lipapi.Invocation{
					Operation:     fl.op,
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
			if referer != "https://flavor.example/" {
				t.Fatalf("HTTP-Referer=%q", referer)
			}
			if title != "FlavorTitle" {
				t.Fatalf("X-OpenRouter-Title=%q", title)
			}
			if sawLegacy {
				t.Fatal("must not emit legacy X-Title")
			}
		})
	}
}

func TestAttribution_proxyModeWireLiterals(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var referer, title string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		referer = r.Header.Get("HTTP-Referer")
		title = r.Header.Get("X-OpenRouter-Title")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"or-test","object":"chat.completion","created":1,"model":"openai/gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	be := openrouter.New(openrouter.Config{
		BaseURL:  srv.URL,
		APIKey:   "sk-test",
		AppURL:   identity.FieldPolicy{Mode: identity.ModeProxy},
		AppTitle: identity.FieldPolicy{Mode: identity.ModeProxy},
	})
	es, err := be.Open(context.Background(), lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIChatCompletions,
			DeliveryMode:  lipapi.DeliveryModeNonStreaming,
			TransportMode: lipapi.TransportModeNonStreaming,
		},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: "openai/gpt-4o-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = es.Close()

	mu.Lock()
	defer mu.Unlock()
	const wantReferer = "https://github.com/matdev83/go-llm-interactive-proxy"
	const wantTitle = "go-llm-interactive-proxy"
	if referer != wantReferer {
		t.Fatalf("HTTP-Referer=%q want %q", referer, wantReferer)
	}
	if title != wantTitle {
		t.Fatalf("X-OpenRouter-Title=%q want %q", title, wantTitle)
	}
}
