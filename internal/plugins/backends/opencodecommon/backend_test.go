package opencodecommon_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/opencodecommon"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/opencodetest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNewBackend_remoteOnlyModelDispatchesWithoutExplicitModels(t *testing.T) {
	t.Parallel()

	var modelsCalls atomic.Int32
	var capture opencodetest.RequestCapture
	srv := newModelsAndFlavorServer(t, &capture, &modelsCalls, `{"data":[{"id":"remote-only-model","name":"Remote Only"}]}`)

	be := opencodecommon.NewBackend(opencodecommon.BackendConfig{
		Kind:       opencodecommon.BackendGo,
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
	})

	es, err := be.Open(context.Background(), nonStreamCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "remote-only-model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasText(drainEvents(t, es), "chat-ns-ok") {
		t.Fatal("expected chat response")
	}
	if got := capture.ModelField(t); got != "remote-only-model" {
		t.Fatalf("model = %q", got)
	}
	if modelsCalls.Load() != 1 {
		t.Fatalf("models calls = %d, want 1", modelsCalls.Load())
	}
}

func TestNewBackend_remoteDiscoveryCachedAcrossOpens(t *testing.T) {
	t.Parallel()

	var modelsCalls atomic.Int32
	var capture opencodetest.RequestCapture
	srv := newModelsAndFlavorServer(t, &capture, &modelsCalls, `{"data":[{"id":"glm-5.2"},{"id":"deepseek-v4"}]}`)

	be := opencodecommon.NewBackend(opencodecommon.BackendConfig{
		Kind:       opencodecommon.BackendGo,
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
	})

	for _, model := range []string{"z-ai/glm-5.2", "deepseek/deepseek-v4"} {
		es, err := be.Open(context.Background(), nonStreamCall(), routing.AttemptCandidate{
			Primary: routing.Primary{Model: model},
		})
		if err != nil {
			t.Fatalf("open %q: %v", model, err)
		}
		drainEvents(t, es)
	}
	if modelsCalls.Load() != 1 {
		t.Fatalf("models calls = %d, want 1", modelsCalls.Load())
	}
}

func TestNewBackend_remoteFailureDoesNotUseBuiltinModelFallback(t *testing.T) {
	t.Parallel()

	var modelsCalls atomic.Int32
	var capture opencodetest.RequestCapture
	srv := newModelsAndFlavorServerWithModelsHandler(t, &capture, &modelsCalls, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusInternalServerError)
	})

	be := opencodecommon.NewBackend(opencodecommon.BackendConfig{
		Kind:       opencodecommon.BackendGo,
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
	})

	_, err := be.Open(context.Background(), nonStreamCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "moonshotai/kimi-k2.7-code"},
	})
	if err == nil {
		t.Fatal("expected remote discovery failure without operator-configured models")
	}
}

func TestNewBackend_staticFallbackDoesNotDisableLaterRemoteResolution(t *testing.T) {
	t.Parallel()

	var modelsCalls atomic.Int32
	var capture opencodetest.RequestCapture
	srv := newModelsAndFlavorServerWithModelsHandler(t, &capture, &modelsCalls, func(w http.ResponseWriter, r *http.Request) {
		if modelsCalls.Load() == 1 {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"remote-only-model","name":"Remote Only"}]}`))
	})

	be := opencodecommon.NewBackend(opencodecommon.BackendConfig{
		Kind:       opencodecommon.BackendGo,
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Models: []opencodecommon.ModelEntry{{
			RawID: "fallback-model",
		}},
	})

	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "fallback-model" {
		t.Fatalf("fallback snapshot = %+v", snap.Models)
	}
	es, err := be.Open(context.Background(), nonStreamCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "remote-only-model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasText(drainEvents(t, es), "chat-ns-ok") {
		t.Fatal("expected chat response")
	}
	if modelsCalls.Load() != 2 {
		t.Fatalf("models calls = %d, want 2", modelsCalls.Load())
	}
}

func TestNewBackend_unknownModelFailsAfterDiscovery(t *testing.T) {
	t.Parallel()

	var modelsCalls atomic.Int32
	srv := newModelsAndFlavorServer(t, nil, &modelsCalls, `{"data":[{"id":"glm-5.2"}]}`)

	be := opencodecommon.NewBackend(opencodecommon.BackendConfig{
		Kind:       opencodecommon.BackendZen,
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
	})

	_, err := be.Open(context.Background(), nonStreamCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "unknown/vendor-model"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, opencodecommon.ErrUnknownModel) && !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("err = %v", err)
	}
	if modelsCalls.Load() != 1 {
		t.Fatalf("models calls = %d, want 1", modelsCalls.Load())
	}
}

func TestNewBackend_nilContext(t *testing.T) {
	t.Parallel()

	be := opencodecommon.NewBackend(opencodecommon.BackendConfig{
		Kind:    opencodecommon.BackendGo,
		BaseURL: "http://127.0.0.1",
		APIKey:  "test-key",
	})
	_, err := be.Open(nil, nonStreamCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "moonshotai/kimi-k2.7-code"},
	})
	if err == nil || !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("err = %v", err)
	}
}

func newModelsAndFlavorServer(t *testing.T, capture *opencodetest.RequestCapture, modelsCalls *atomic.Int32, modelsBody string) *httptest.Server {
	t.Helper()
	return newModelsAndFlavorServerWithModelsHandler(t, capture, modelsCalls, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(modelsBody))
	})
}

func newModelsAndFlavorServerWithModelsHandler(t *testing.T, capture *opencodetest.RequestCapture, modelsCalls *atomic.Int32, modelsHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	if capture == nil {
		capture = &opencodetest.RequestCapture{}
	}
	flavor := opencodetest.NewFlavorServer(t, capture)
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		modelsCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		modelsHandler(w, r)
	})
	mux.Handle("/", flavor.Config.Handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func nonStreamCall() lipapi.Call {
	return lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
		Invocation: lipapi.Invocation{
			DeliveryMode:  lipapi.DeliveryModeNonStreaming,
			TransportMode: lipapi.TransportModeNonStreaming,
		},
	}
}

func drainEvents(t *testing.T, es lipapi.ManagedEventStream) []lipapi.Event {
	t.Helper()
	defer func() { _ = es.Close() }()
	out := []lipapi.Event{}
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		out = append(out, ev)
	}
}

func hasText(events []lipapi.Event, want string) bool {
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, want) {
			return true
		}
	}
	return false
}
