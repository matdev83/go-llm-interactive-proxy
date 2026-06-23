package ollama_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/ollama"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/ollama"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func testCandidate(model string) routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Model: model}}
}

func boolPtr(v bool) *bool { return &v }

func testCall(ext map[string]json.RawMessage) lipapi.Call {
	return lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
		}},
		Extensions: ext,
	}
}

func drainStream(t *testing.T, es lipapi.ManagedEventStream) []lipapi.Event {
	t.Helper()
	ctx := context.Background()
	var events []lipapi.Event
	for {
		ev, err := es.Recv(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		events = append(events, ev)
	}
	_ = es.Close()
	return events
}

func newRefServer(t *testing.T, cfg refbackend.Config) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(refbackend.NewHandler(cfg))
	t.Cleanup(srv.Close)
	return srv
}

func TestIntegration_noCredentialsSendsDummyBearer(t *testing.T) {
	t.Parallel()
	var auth string
	srv := newRefServer(t, refbackend.Config{
		Version:            "0.13.3",
		ResponsesSupported: true,
		OnRequest: func(r *http.Request, _ []byte) {
			auth = r.Header.Get("Authorization")
		},
	})

	be := ollama.New(ollama.Config{
		BaseURL:       srv.URL + "/v1",
		SDKMaxRetries: new(int),
		HTTPClient:    srv.Client(),
	})

	call := testCall(nil)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	es, err := be.Open(context.Background(), call, testCandidate("ollama/llama3:latest"))
	if err != nil {
		t.Fatal(err)
	}
	_ = drainStream(t, es)
	if auth != "Bearer ollama" {
		t.Fatalf("Authorization = %q", auth)
	}
}

func TestIntegration_explicitAPIKeyPreserved(t *testing.T) {
	t.Parallel()
	var auth string
	srv := newRefServer(t, refbackend.Config{
		Version:            "0.13.3",
		ResponsesSupported: true,
		OnRequest: func(r *http.Request, _ []byte) {
			auth = r.Header.Get("Authorization")
		},
	})

	be := ollama.New(ollama.Config{
		BaseURL:       srv.URL + "/v1",
		APIKey:        "my-secret",
		SDKMaxRetries: new(int),
		HTTPClient:    srv.Client(),
	})

	call := testCall(nil)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	es, err := be.Open(context.Background(), call, testCandidate("ollama/llama3:latest"))
	if err != nil {
		t.Fatal(err)
	}
	_ = drainStream(t, es)
	if auth != "Bearer my-secret" {
		t.Fatalf("Authorization = %q", auth)
	}
}

func TestIntegration_chatCompletionsNonStream(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{Version: "0.13.3", ResponsesSupported: true})

	call := testCall(nil)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	be := ollama.New(ollama.Config{
		BaseURL:       srv.URL + "/v1",
		SDKMaxRetries: new(int),
		HTTPClient:    srv.Client(),
	})

	es, err := be.Open(context.Background(), call, testCandidate("ollama/llama3:latest"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)
	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, "ollama-ok") {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected text delta with ollama-ok")
	}
}

func TestIntegration_chatCompletionsStreaming(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{Version: "0.13.3", ResponsesSupported: true})

	call := testCall(nil)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeStreaming,
		TransportMode: lipapi.TransportModeStreaming,
	}
	be := ollama.New(ollama.Config{
		BaseURL:       srv.URL + "/v1",
		SDKMaxRetries: new(int),
		HTTPClient:    srv.Client(),
	})

	es, err := be.Open(context.Background(), call, testCandidate("ollama/llama3:latest"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)
	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, "ollama-stream-ok") {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected text delta with ollama-stream-ok")
	}
}

func responsesTestCall(ext map[string]json.RawMessage) lipapi.Call {
	if ext == nil {
		ext = map[string]json.RawMessage{
			openrouterwire.ExtUpstreamFlavor: json.RawMessage(`"responses"`),
		}
	}
	call := testCall(ext)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIResponses,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	return call
}

func TestIntegration_responsesEnabledWithSupportedVersion(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{Version: "0.13.3", ResponsesSupported: true})

	be := ollama.New(ollama.Config{
		BaseURL:       srv.URL + "/v1",
		ResponsesAPI:  "enabled",
		SDKMaxRetries: new(int),
		HTTPClient:    srv.Client(),
	})

	es, err := be.Open(context.Background(), responsesTestCall(nil), testCandidate("ollama/llama3:latest"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)
	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, "ollama-ok") {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected responses text delta")
	}
}

func TestIntegration_explicitResponsesRejectedWhenDisabled(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var paths []string
	srv := newRefServer(t, refbackend.Config{
		Version:            "0.13.3",
		ResponsesSupported: true,
		OnRequest: func(r *http.Request, _ []byte) {
			mu.Lock()
			paths = append(paths, r.URL.Path)
			mu.Unlock()
		},
	})

	be := ollama.New(ollama.Config{
		BaseURL:      srv.URL + "/v1",
		ResponsesAPI: "disabled",
		HTTPClient:   srv.Client(),
	})

	_, err := be.Open(context.Background(), responsesTestCall(nil), testCandidate("ollama/llama3:latest"))
	if err == nil {
		t.Fatal("expected error for explicit responses when disabled")
	}
	if !strings.Contains(err.Error(), "responses") {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if p == "/v1/responses" {
			t.Fatalf("unexpected /v1/responses hit, paths=%v", paths)
		}
	}
}

func TestIntegration_explicitResponsesRejectedForOldVersion(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var paths []string
	srv := newRefServer(t, refbackend.Config{
		Version:            "0.13.2",
		ResponsesSupported: false,
		OnRequest: func(r *http.Request, _ []byte) {
			mu.Lock()
			paths = append(paths, r.URL.Path)
			mu.Unlock()
		},
	})

	be := ollama.New(ollama.Config{
		BaseURL:      srv.URL + "/v1",
		ResponsesAPI: "auto",
		HTTPClient:   srv.Client(),
	})

	_, err := be.Open(context.Background(), responsesTestCall(nil), testCandidate("ollama/llama3:latest"))
	if err == nil {
		t.Fatal("expected error for explicit responses on old Ollama version")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if p == "/v1/responses" {
			t.Fatalf("unexpected /v1/responses hit, paths=%v", paths)
		}
	}
}

func TestIntegration_chatCompletionsPayloadMutation_maxTokensRemap(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedBody string

	srv := newRefServer(t, refbackend.Config{
		Version:            "0.13.3",
		ResponsesSupported: true,
		OnRequestBody: func(body []byte) {
			mu.Lock()
			capturedBody = string(body)
			mu.Unlock()
		},
	})

	maxTokens := 1024
	call := testCall(nil)
	call.Options.MaxOutputTokens = &maxTokens
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	be := ollama.New(ollama.Config{
		BaseURL:       srv.URL + "/v1",
		SDKMaxRetries: new(int),
		HTTPClient:    srv.Client(),
	})

	es, err := be.Open(context.Background(), call, testCandidate("ollama/llama3:latest"))
	if err != nil {
		t.Fatal(err)
	}
	_ = drainStream(t, es)

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(capturedBody, `"max_tokens"`) {
		t.Fatalf("captured body must contain max_tokens, body=%s", capturedBody)
	}
	if strings.Contains(capturedBody, `"max_completion_tokens"`) {
		t.Fatalf("captured body must not contain max_completion_tokens, body=%s", capturedBody)
	}
}

func TestIntegration_chatCompletionsStreaming_preservesStreamOptions(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedBody string

	srv := newRefServer(t, refbackend.Config{
		Version:            "0.13.3",
		ResponsesSupported: true,
		OnRequestBody: func(body []byte) {
			mu.Lock()
			capturedBody = string(body)
			mu.Unlock()
		},
	})

	call := testCall(nil)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeStreaming,
		TransportMode: lipapi.TransportModeStreaming,
	}
	be := ollama.New(ollama.Config{
		BaseURL:       srv.URL + "/v1",
		SDKMaxRetries: new(int),
		HTTPClient:    srv.Client(),
	})

	es, err := be.Open(context.Background(), call, testCandidate("ollama/llama3:latest"))
	if err != nil {
		t.Fatal(err)
	}
	_ = drainStream(t, es)

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(capturedBody, `"stream_options"`) {
		t.Fatalf("captured body must contain stream_options, body=%s", capturedBody)
	}
	if !strings.Contains(capturedBody, `"include_usage"`) {
		t.Fatalf("captured body must contain include_usage, body=%s", capturedBody)
	}
}

func TestIntegration_transportCaps_responsesDisabledForOldVersion(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{Version: "0.13.2", ResponsesSupported: false})

	be := ollama.New(ollama.Config{
		BaseURL:      srv.URL + "/v1",
		ResponsesAPI: "auto",
		HTTPClient:   srv.Client(),
	})
	caps := be.TransportCaps
	if caps.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeStreaming) {
		t.Fatal("expected responses streaming unsupported")
	}
	if caps.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeNonStreaming) {
		t.Fatal("expected responses non-streaming unsupported")
	}
	if !caps.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeStreaming) {
		t.Fatal("expected chat streaming supported")
	}
}

func TestIntegration_transportCaps_responsesDisabledMode(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{Version: "0.13.3", ResponsesSupported: true})

	be := ollama.New(ollama.Config{
		BaseURL:      srv.URL + "/v1",
		ResponsesAPI: "disabled",
		HTTPClient:   srv.Client(),
	})
	caps := be.TransportCaps
	if caps.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeNonStreaming) {
		t.Fatal("expected responses unsupported in disabled mode")
	}
}

func TestIntegration_cloudModelResolverAppendsCloudSuffix(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedBody string

	srv := newRefServer(t, refbackend.Config{
		Version:            "0.13.3",
		ResponsesSupported: true,
		OnRequestBody: func(body []byte) {
			mu.Lock()
			capturedBody = string(body)
			mu.Unlock()
		},
	})

	be := ollama.NewCloud(ollama.Config{
		BaseURL:       srv.URL + "/v1",
		SDKMaxRetries: new(int),
		HTTPClient:    srv.Client(),
	})

	call := testCall(nil)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	es, err := be.Open(context.Background(), call, testCandidate("google/gemma3:4b"))
	if err != nil {
		t.Fatal(err)
	}
	_ = drainStream(t, es)

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(capturedBody, `"model":"gemma3:4b-cloud"`) {
		t.Fatalf("body = %s", capturedBody)
	}
}

func TestIntegration_localCanonicalModelPassedUpstreamWithoutVendorPrefix(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedBody string

	srv := newRefServer(t, refbackend.Config{
		Version:            "0.13.3",
		ResponsesSupported: true,
		OnRequestBody: func(body []byte) {
			mu.Lock()
			capturedBody = string(body)
			mu.Unlock()
		},
	})

	be := ollama.New(ollama.Config{
		BaseURL:       srv.URL + "/v1",
		SDKMaxRetries: new(int),
		HTTPClient:    srv.Client(),
	})

	call := testCall(nil)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	es, err := be.Open(context.Background(), call, testCandidate("ollama:google/gemma3:4b"))
	if err != nil {
		t.Fatal(err)
	}
	_ = drainStream(t, es)

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(capturedBody, `"model":"gemma3:4b"`) {
		t.Fatalf("body = %s", capturedBody)
	}
}

func TestIntegration_cloudCanonicalModelPassedUpstreamWithCloudSuffix(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedBody string

	srv := newRefServer(t, refbackend.Config{
		Version:            "0.13.3",
		ResponsesSupported: true,
		OnRequestBody: func(body []byte) {
			mu.Lock()
			capturedBody = string(body)
			mu.Unlock()
		},
	})

	be := ollama.NewCloud(ollama.Config{
		BaseURL:       srv.URL + "/v1",
		SDKMaxRetries: new(int),
		HTTPClient:    srv.Client(),
	})

	call := testCall(nil)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	es, err := be.Open(context.Background(), call, testCandidate("ollama-cloud:google/gemma3:4b"))
	if err != nil {
		t.Fatal(err)
	}
	_ = drainStream(t, es)

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(capturedBody, `"model":"gemma3:4b-cloud"`) {
		t.Fatalf("body = %s", capturedBody)
	}
}

func TestIntegration_cloudInventoryAdvertisesWithoutCloudSuffix(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{Version: "0.13.3"})
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"gemma4:31b-cloud"}]}`))
	}))
	t.Cleanup(cloud.Close)

	be := ollama.NewCloud(ollama.Config{
		BaseURL:    srv.URL + "/v1",
		HTTPClient: srv.Client(),
		Discovery: ollama.DiscoveryConfig{
			CloudURL:     cloud.URL,
			Catalog:      boolPtr(false),
			Capabilities: boolPtr(false),
		},
	})

	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 {
		t.Fatalf("models = %+v", snap.Models)
	}
	if snap.Models[0].CanonicalID != "google/gemma4:31b" {
		t.Fatalf("CanonicalID = %q", snap.Models[0].CanonicalID)
	}
	if snap.Models[0].NativeID != "gemma4:31b" {
		t.Fatalf("NativeID = %q", snap.Models[0].NativeID)
	}
}

func TestIntegration_cloudModelIDPassedUpstreamUnchangedWhenAlreadySuffixed(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedBody string

	srv := newRefServer(t, refbackend.Config{
		Version:            "0.13.3",
		ResponsesSupported: true,
		OnRequestBody: func(body []byte) {
			mu.Lock()
			capturedBody = string(body)
			mu.Unlock()
		},
	})

	be := ollama.NewCloud(ollama.Config{
		BaseURL:       srv.URL + "/v1",
		SDKMaxRetries: new(int),
		HTTPClient:    srv.Client(),
	})

	call := testCall(nil)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	es, err := be.Open(context.Background(), call, testCandidate("ollama-cloud/deepseek-v3.2-cloud"))
	if err != nil {
		t.Fatal(err)
	}
	_ = drainStream(t, es)

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(capturedBody, `"model":"deepseek-v3.2-cloud"`) {
		t.Fatalf("body = %s", capturedBody)
	}
}

func TestIntegration_resolveCapsUsesProbedCapabilities(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{
		Version:     "0.13.3",
		LocalModels: []string{"llama3.2:latest"},
		Capabilities: map[string][]string{
			"llama3.2:latest": {"completion"},
		},
	})

	be := ollama.New(ollama.Config{
		BaseURL:    srv.URL + "/v1",
		HTTPClient: srv.Client(),
	})
	if _, err := be.ModelInventory.LoadModels(context.Background()); err != nil {
		t.Fatal(err)
	}

	call := testCall(nil)
	caps := be.ResolveCaps(context.Background(), call, testCandidate("ollama/llama3.2:latest"))
	if _, ok := caps[lipapi.CapabilityStreaming]; !ok {
		t.Fatal("expected streaming")
	}
	if _, ok := caps[lipapi.CapabilityVision]; ok {
		t.Fatal("vision must be absent for completion-only model")
	}
}

func TestIntegration_resolveCapsLazilyProbesRequestedModel(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{
		Version: "0.13.3",
		Capabilities: map[string][]string{
			"gemma3:4b-cloud": {"completion", "vision"},
		},
	})

	be := ollama.NewCloud(ollama.Config{
		BaseURL:    srv.URL + "/v1",
		HTTPClient: srv.Client(),
	})

	call := testCall(nil)
	caps := be.ResolveCaps(context.Background(), call, testCandidate("google/gemma3:4b"))
	if _, ok := caps[lipapi.CapabilityVision]; !ok {
		t.Fatal("expected lazy /api/show probe to discover vision capability")
	}
}
