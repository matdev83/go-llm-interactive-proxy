package opencodezen_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/opencodecommon"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/opencodezen"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/opencodetest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func zenTestModels(base string) []opencodecommon.ModelEntry {
	return []opencodecommon.ModelEntry{
		{
			RawID:        "kimi-k2.7-code",
			Endpoint:     base + "/v1/chat/completions",
			AISDKPackage: "@ai-sdk/openai-compatible",
		},
		{
			RawID:        "gpt-5.4",
			Endpoint:     base + "/v1/responses",
			AISDKPackage: "@ai-sdk/openai",
		},
		{
			RawID:        "gemini-3.1-pro",
			Endpoint:     base + "/v1beta/models/gemini-3.1-pro",
			AISDKPackage: "@ai-sdk/google",
		},
	}
}

func TestNew_kimiRoutesOpenAIChatWithBearerAuth(t *testing.T) {
	t.Parallel()

	var capture opencodetest.RequestCapture
	srv := opencodetest.NewFlavorServer(t, &capture)
	be := opencodezen.New(opencodezen.Config{
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Models:     zenTestModels(srv.URL),
	})

	es, err := be.Open(context.Background(), nonStreamCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "moonshotai/kimi-k2.7-code"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasText(drainEvents(t, es), "chat-ns-ok") {
		t.Fatal("expected chat response")
	}
	if !strings.HasSuffix(capture.Path, "/v1/chat/completions") {
		t.Fatalf("path = %q", capture.Path)
	}
	if capture.Authorization != "Bearer test-key" {
		t.Fatalf("authorization = %q", capture.Authorization)
	}
	if got := capture.ModelField(t); got != "kimi-k2.7-code" {
		t.Fatalf("model = %q", got)
	}
}

func TestNew_gptRoutesResponsesWithBearerAuth(t *testing.T) {
	t.Parallel()

	var capture opencodetest.RequestCapture
	srv := opencodetest.NewFlavorServer(t, &capture)
	be := opencodezen.New(opencodezen.Config{
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Models:     zenTestModels(srv.URL),
	})

	es, err := be.Open(context.Background(), nonStreamCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "openai/gpt-5.4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasText(drainEvents(t, es), "responses-ns-ok") {
		t.Fatal("expected responses output")
	}
	if !strings.HasSuffix(capture.Path, "/v1/responses") {
		t.Fatalf("path = %q", capture.Path)
	}
	if capture.Authorization != "Bearer test-key" {
		t.Fatalf("authorization = %q", capture.Authorization)
	}
	if got := capture.ModelField(t); got != "gpt-5.4" {
		t.Fatalf("model = %q", got)
	}
}

func TestNew_geminiRoutesGoogleWithAPIKeyHeader(t *testing.T) {
	t.Parallel()

	var capture opencodetest.RequestCapture
	srv := opencodetest.NewFlavorServer(t, &capture)
	be := opencodezen.New(opencodezen.Config{
		BaseURL:    srv.URL,
		APIKey:     "google-test-key",
		HTTPClient: srv.Client(),
		Models:     zenTestModels(srv.URL),
	})

	es, err := be.Open(context.Background(), nonStreamCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "google/gemini-3.1-pro"},
	})
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if !col.FinishReceived {
		t.Fatal("expected finish")
	}
	if !strings.Contains(capture.Path, "GenerateContent") {
		t.Fatalf("path = %q", capture.Path)
	}
	if capture.GoogleAPIKey != "google-test-key" {
		t.Fatalf("x-goog-api-key = %q", capture.GoogleAPIKey)
	}
}

func TestNew_unknownModelFailsExplicitly(t *testing.T) {
	t.Parallel()

	var capture opencodetest.RequestCapture
	srv := opencodetest.NewFlavorServer(t, &capture)
	be := opencodezen.New(opencodezen.Config{
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Models:     zenTestModels(srv.URL),
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
}

func TestNew_withoutExplicitModelsRequiresRemoteDiscovery(t *testing.T) {
	t.Parallel()

	var capture opencodetest.RequestCapture
	srv := opencodetest.NewFlavorServer(t, &capture)
	be := opencodezen.New(opencodezen.Config{
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
	})

	_, err := be.Open(context.Background(), nonStreamCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "openai/gpt-5.4"},
	})
	if err == nil {
		t.Fatal("expected remote discovery failure without explicit models")
	}
}

func TestNew_transportCapsExposeOpenAIOperations(t *testing.T) {
	t.Parallel()

	be := opencodezen.New(opencodezen.Config{
		BaseURL: "http://127.0.0.1",
		APIKey:  "test-key",
	})
	caps := execbackend.EffectiveTransportCaps(context.Background(), be, lipapi.Call{
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions},
	}, routing.AttemptCandidate{})
	if !caps.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeNonStreaming) {
		t.Fatal("chat non-streaming must be supported")
	}
	if !caps.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeStreaming) {
		t.Fatal("responses streaming must be supported")
	}
}

func TestNew_gptStreamingHappyPath(t *testing.T) {
	t.Parallel()

	var capture opencodetest.RequestCapture
	srv := opencodetest.NewFlavorServer(t, &capture)
	be := opencodezen.New(opencodezen.Config{
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Models:     zenTestModels(srv.URL),
	})

	call := lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
		Invocation: lipapi.Invocation{
			DeliveryMode:  lipapi.DeliveryModeStreaming,
			TransportMode: lipapi.TransportModeStreaming,
		},
	}
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "openai/gpt-5.4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasText(drainEvents(t, es), "responses-stream-ok") {
		t.Fatal("expected streaming responses output")
	}
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
