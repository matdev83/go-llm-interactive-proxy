package lmstudio_test

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
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/lmstudio"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/nvidia"
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

func responsesTestCall() lipapi.Call {
	call := testCall()
	call.Extensions = map[string]json.RawMessage{
		openrouterwire.ExtUpstreamFlavor: json.RawMessage(`"responses"`),
	}
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIResponses,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	return call
}

func TestNew_transportCaps_chatCompletionsOnly(t *testing.T) {
	t.Parallel()

	be := lmstudio.New(lmstudio.Config{BaseURL: "http://127.0.0.1:1"})
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
	if !caps.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeNonStreaming) {
		t.Fatal("expected chat non-streaming supported")
	}

	resolved := be.ResolveTransportCaps(context.Background(), testCall(), testCandidate("lmstudio:local-model"))
	if resolved.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeNonStreaming) {
		t.Fatal("ResolveTransportCaps must not advertise responses")
	}
}

func TestNew_openRejectsResponsesAPI(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var paths []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		refbackend.NewHandler(refbackend.Config{AllowMissingBearer: true}).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	be := lmstudio.New(lmstudio.Config{
		BaseURL:       srv.URL,
		SDKMaxRetries: new(int),
	})

	_, err := be.Open(context.Background(), responsesTestCall(), testCandidate("lmstudio:local-model"))
	if err == nil {
		t.Fatal("expected error for responses API")
	}
	if !strings.Contains(err.Error(), "lmstudio: responses API is not available") {
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

func TestNew_buildsBackendIDAndChatCompletions(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedModel string

	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
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
	}))
	t.Cleanup(srv.Close)

	be := lmstudio.New(lmstudio.Config{
		BaseURL:       srv.URL,
		SDKMaxRetries: new(int),
	})
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != lmstudio.ID {
		t.Fatalf("BackendPrefixes = %#v", be.BackendPrefixes)
	}

	es, err := be.Open(context.Background(), testCall(), testCandidate("lmstudio:local-model"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)
	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, "nvidia-ok") {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected text delta")
	}

	mu.Lock()
	defer mu.Unlock()
	if capturedModel != "local-model" {
		t.Fatalf("upstream model = %q", capturedModel)
	}
}

func TestNew_inventoryMapsCatalogAndFallsBack(t *testing.T) {
	t.Parallel()

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-oss:120b"},{"id":"unknown-local"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	catalogSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"openai":{"id":"openai","models":[{"id":"gpt-oss:120b"}]}}`))
	}))
	t.Cleanup(catalogSrv.Close)

	be := lmstudio.New(lmstudio.Config{
		BaseURL:    modelsSrv.URL,
		HTTPClient: modelsSrv.Client(),
		Discovery: lmstudio.DiscoveryConfig{
			CatalogURL: catalogSrv.URL,
		},
	})
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"gpt-oss:120b":  "openai/gpt-oss:120b",
		"unknown-local": "lmstudio/unknown-local",
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
