package vllm_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/vllm"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/vllm"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func testCandidate(model string) routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Model: model}}
}

func testCall() lipapi.Call {
	return lipapi.Call{
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
}

func responsesTestCall(streaming bool) lipapi.Call {
	call := testCall()
	call.Extensions = map[string]json.RawMessage{
		openrouterwire.ExtUpstreamFlavor: json.RawMessage(`"responses"`),
	}
	if streaming {
		call.Invocation = lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIResponses,
			DeliveryMode:  lipapi.DeliveryModeStreaming,
			TransportMode: lipapi.TransportModeStreaming,
		}
	} else {
		call.Invocation = lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIResponses,
			DeliveryMode:  lipapi.DeliveryModeNonStreaming,
			TransportMode: lipapi.TransportModeNonStreaming,
		}
	}
	return call
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

func TestVllmBackend_transportCaps(t *testing.T) {
	t.Parallel()

	be := vllm.New(vllm.Config{BaseURL: "http://127.0.0.1:1"})
	caps := be.TransportCaps
	if !caps.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeStreaming) {
		t.Fatal("expected chat streaming supported")
	}
	if !caps.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeNonStreaming) {
		t.Fatal("expected chat non-streaming supported")
	}
	if !caps.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeStreaming) {
		t.Fatal("expected responses streaming supported")
	}
	if !caps.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeNonStreaming) {
		t.Fatal("expected responses non-streaming supported")
	}

	resolved := be.ResolveTransportCaps(context.Background(), testCall(), testCandidate("vllm/meta-llama/Llama-3-8B-Instruct"))
	if !resolved.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeNonStreaming) {
		t.Fatal("ResolveTransportCaps must advertise responses")
	}
}

func TestVllmBackend_backendID(t *testing.T) {
	t.Parallel()

	be := vllm.New(vllm.Config{BaseURL: "http://127.0.0.1:1"})
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != vllm.ID {
		t.Fatalf("BackendPrefixes = %#v", be.BackendPrefixes)
	}
}

func TestVllmBackend_chatNonStream(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedModel string

	srv := newRefServer(t, refbackend.Config{
		OnRequestBody: func(b []byte) {
			mu.Lock()
			defer mu.Unlock()
			var payload struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(b, &payload)
			capturedModel = payload.Model
		},
		AllowMissingBearer: true,
	})

	be := vllm.New(vllm.Config{
		BaseURL:       srv.URL,
		SDKMaxRetries: new(int),
	})

	es, err := be.Open(context.Background(), testCall(), testCandidate("vllm/meta-llama/Llama-3-8B-Instruct"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)
	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, "vllm-ok") {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected text delta")
	}

	mu.Lock()
	defer mu.Unlock()
	if capturedModel != "meta-llama/Llama-3-8B-Instruct" {
		t.Fatalf("upstream model = %q", capturedModel)
	}
}

func TestVllmBackend_chatStream(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedBody string

	srv := newRefServer(t, refbackend.Config{
		OnRequestBody: func(b []byte) {
			mu.Lock()
			capturedBody = string(b)
			mu.Unlock()
		},
		AllowMissingBearer: true,
	})

	call := testCall()
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeStreaming,
		TransportMode: lipapi.TransportModeStreaming,
	}
	be := vllm.New(vllm.Config{
		BaseURL:       srv.URL,
		SDKMaxRetries: new(int),
	})

	es, err := be.Open(context.Background(), call, testCandidate("vllm/meta-llama/Llama-3-8B-Instruct"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)
	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, "vllm-ok") {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected text delta")
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(capturedBody, `"stream":true`) {
		t.Fatalf("streaming must set stream:true, body=%s", capturedBody)
	}
}

func TestVllmBackend_responsesNonStream(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{AllowMissingBearer: true})

	be := vllm.New(vllm.Config{
		BaseURL:       srv.URL,
		SDKMaxRetries: new(int),
	})

	es, err := be.Open(context.Background(), responsesTestCall(false), testCandidate("vllm/meta-llama/Llama-3-8B-Instruct"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)
	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, "vllm-ok") {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected text delta with 'vllm-ok'")
	}
}

func TestVllmBackend_responsesStream(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{AllowMissingBearer: true})

	be := vllm.New(vllm.Config{
		BaseURL:       srv.URL,
		SDKMaxRetries: new(int),
	})

	es, err := be.Open(context.Background(), responsesTestCall(true), testCandidate("vllm/meta-llama/Llama-3-8B-Instruct"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)
	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, "vllm-ok") {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected text delta with 'vllm-ok'")
	}
}

func TestVllmBackend_inventoryWithCatalog(t *testing.T) {
	t.Parallel()

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"Llama-3-8B-Instruct"},{"id":"unknown-local"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	catalogSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"meta-llama":{"id":"meta-llama","models":[{"id":"Llama-3-8B-Instruct"}]}}`))
	}))
	t.Cleanup(catalogSrv.Close)

	be := vllm.New(vllm.Config{
		BaseURL:    modelsSrv.URL,
		HTTPClient: modelsSrv.Client(),
		Discovery: vllm.DiscoveryConfig{
			CatalogURL: catalogSrv.URL,
		},
	})
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Llama-3-8B-Instruct": "meta-llama/Llama-3-8B-Instruct",
		"unknown-local":       "vllm/unknown-local",
	}
	for _, model := range snap.Models {
		if got, ok := want[model.NativeID]; !ok || model.CanonicalID != got {
			t.Fatalf("model = %+v", model)
		}
		delete(want, model.NativeID)
	}
	if len(want) != 0 {
		t.Fatalf("missing models: %+v", want)
	}
}

func TestVllmBackend_inventoryPreservesVendorID(t *testing.T) {
	t.Parallel()

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"meta-llama/Llama-3-8B-Instruct"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	catalogSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"other-vendor":{"id":"other-vendor","models":[{"id":"other-vendor/other-model"}]}}`))
	}))
	t.Cleanup(catalogSrv.Close)

	be := vllm.New(vllm.Config{
		BaseURL:    modelsSrv.URL,
		HTTPClient: modelsSrv.Client(),
		Discovery: vllm.DiscoveryConfig{
			CatalogURL: catalogSrv.URL,
		},
	})
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 {
		t.Fatalf("models = %+v", snap.Models)
	}
	model := snap.Models[0]
	if model.NativeID != "meta-llama/Llama-3-8B-Instruct" {
		t.Fatalf("NativeID = %q", model.NativeID)
	}
	if model.CanonicalID != "meta-llama/Llama-3-8B-Instruct" {
		t.Fatalf("CanonicalID = %q, want preserved vendor ID", model.CanonicalID)
	}
}

func TestVllmBackend_authFailure(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{
		ForcedHTTPStatus: http.StatusUnauthorized,
	})

	be := vllm.New(vllm.Config{
		BaseURL:       srv.URL,
		APIKeys:       []string{"bad-key"},
		SDKMaxRetries: new(int),
	})

	_, err := be.Open(context.Background(), testCall(), testCandidate("vllm/meta-llama/Llama-3-8B-Instruct"))
	if err == nil {
		t.Fatal("expected error for auth failure")
	}
	if !errors.Is(err, lipapi.ErrRecoverablePreOutput) {
		t.Fatalf("expected recoverable pre-output auth error, got %v", err)
	}
}

func TestVllmBackend_rateLimitClassification(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{
		ForcedHTTPStatus: http.StatusTooManyRequests,
		ForcedRetryAfter: "60",
	})

	be := vllm.New(vllm.Config{
		BaseURL:       srv.URL,
		APIKeys:       []string{"key-1"},
		SDKMaxRetries: new(int),
	})

	_, err := be.Open(context.Background(), testCall(), testCandidate("vllm/meta-llama/Llama-3-8B-Instruct"))
	if err == nil {
		t.Fatal("expected error for rate limit")
	}
	if !errors.Is(err, lipapi.ErrRecoverablePreOutput) {
		t.Fatalf("expected recoverable pre-output rate limit error, got %v", err)
	}
}
