package nvidia_test

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
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/nvidia"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/nvidia"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func testCandidate(model string) routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Model: model}}
}

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

func intPtr(n int) *int { return &n }

// --- Chat Completions Tests ---

func TestIntegration_chatCompletionsNonStream(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedBody string

	srv := newRefServer(t, refbackend.Config{
		OnRequestBody: func(b []byte) {
			mu.Lock()
			capturedBody = string(b)
			mu.Unlock()
		},
	})

	call := testCall(nil)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	be := nvidia.New(nvidia.Config{
		BaseURL:       srv.URL,
		APIKey:        "nvapi-test",
		SDKMaxRetries: intPtr(0),
	})

	es, err := be.Open(context.Background(), call, testCandidate("nvidia/llama-3.1-nemotron-nano-8b-v1"))
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
		t.Fatal("expected text delta with 'nvidia-ok'")
	}

	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(capturedBody, `"stream":true`) {
		t.Fatalf("non-streaming must not set stream:true, body=%s", capturedBody)
	}
}

func TestIntegration_chatCompletionsStreaming(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedBody string

	srv := newRefServer(t, refbackend.Config{
		OnRequestBody: func(b []byte) {
			mu.Lock()
			capturedBody = string(b)
			mu.Unlock()
		},
	})

	call := testCall(nil)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeStreaming,
		TransportMode: lipapi.TransportModeStreaming,
	}
	be := nvidia.New(nvidia.Config{
		BaseURL:       srv.URL,
		APIKey:        "nvapi-test",
		SDKMaxRetries: intPtr(0),
	})

	es, err := be.Open(context.Background(), call, testCandidate("nvidia/llama-3.1-nemotron-nano-8b-v1"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)

	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, "nvidia-stream-ok") {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected text delta with 'nvidia-stream-ok'")
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(capturedBody, `"stream":true`) {
		t.Fatalf("streaming must set stream:true, body=%s", capturedBody)
	}
}

func TestIntegration_chatNonStreamUsage(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{
		ChatNonStreamJSON: `{
  "id": "gen-nvidia-refbackend-usage",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "nvidia/llama-3.1-nemotron-nano-8b-v1",
  "choices": [{"index":0,"message":{"role":"assistant","content":"nvidia-ok"},"finish_reason":"stop"}],
  "usage": {
    "prompt_tokens": 3,
    "completion_tokens": 7,
    "total_tokens": 10,
    "completion_tokens_details": {"reasoning_tokens": 5}
  }
}`,
	})

	call := testCall(nil)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	be := nvidia.New(nvidia.Config{
		BaseURL:       srv.URL,
		APIKey:        "nvapi-test",
		SDKMaxRetries: intPtr(0),
	})

	es, err := be.Open(context.Background(), call, testCandidate("nvidia/llama-3.1-nemotron-nano-8b-v1"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)

	var usage lipapi.Event
	found := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventUsageDelta {
			usage = ev
			found = true
		}
	}
	if !found {
		t.Fatal("expected usage delta event")
	}
	if usage.InputTokens != 3 {
		t.Fatalf("InputTokens = %d, want 3", usage.InputTokens)
	}
	if usage.OutputTokens != 7 {
		t.Fatalf("OutputTokens = %d, want 7", usage.OutputTokens)
	}
	if usage.TotalTokens != 10 {
		t.Fatalf("TotalTokens = %d, want 10", usage.TotalTokens)
	}
	if usage.ReasoningTokens != 5 {
		t.Fatalf("ReasoningTokens = %d, want 5", usage.ReasoningTokens)
	}
}

func TestIntegration_chatStreamUsage(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{
		ChatStreamSSE: refbackend.ChatStreamWithUsageSSE,
	})

	call := testCall(nil)
	be := nvidia.New(nvidia.Config{
		BaseURL:       srv.URL,
		APIKey:        "nvapi-test",
		SDKMaxRetries: intPtr(0),
	})

	es, err := be.Open(context.Background(), call, testCandidate("nvidia/llama-3.1-nemotron-nano-8b-v1"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)
	hasUsage := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventUsageDelta {
			hasUsage = true
			if ev.InputTokens == 0 && ev.OutputTokens == 0 {
				t.Error("usage delta has zero tokens")
			}
		}
	}
	if !hasUsage {
		t.Fatal("expected usage delta event")
	}
}

func TestIntegration_chatCompletionsPayloadMutation_streamOptionsStripped(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedBody string

	srv := newRefServer(t, refbackend.Config{
		OnRequestBody: func(b []byte) {
			mu.Lock()
			capturedBody = string(b)
			mu.Unlock()
		},
	})

	call := testCall(nil)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	be := nvidia.New(nvidia.Config{
		BaseURL:       srv.URL,
		APIKey:        "nvapi-test",
		SDKMaxRetries: intPtr(0),
	})

	es, err := be.Open(context.Background(), call, testCandidate("nvidia/llama-3.1-nemotron-nano-8b-v1"))
	if err != nil {
		t.Fatal(err)
	}
	_ = drainStream(t, es)

	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(capturedBody, `"stream_options"`) {
		t.Fatalf("captured body must not contain stream_options, body=%s", capturedBody)
	}
}

func TestIntegration_chatCompletionsPayloadMutation_maxTokensRemap(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedBody string

	srv := newRefServer(t, refbackend.Config{
		OnRequestBody: func(b []byte) {
			mu.Lock()
			capturedBody = string(b)
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
	be := nvidia.New(nvidia.Config{
		BaseURL:       srv.URL,
		APIKey:        "nvapi-test",
		SDKMaxRetries: intPtr(0),
	})

	es, err := be.Open(context.Background(), call, testCandidate("nvidia/llama-3.1-nemotron-nano-8b-v1"))
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

func TestIntegration_chatCompletionsExtraBody(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedBody string

	srv := newRefServer(t, refbackend.Config{
		OnRequestBody: func(b []byte) {
			mu.Lock()
			capturedBody = string(b)
			mu.Unlock()
		},
	})

	call := testCall(map[string]json.RawMessage{
		"nvidia.extra_body.chat_template_kwargs": json.RawMessage(`{"enable_thinking":true}`),
	})
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	be := nvidia.New(nvidia.Config{
		BaseURL:       srv.URL,
		APIKey:        "nvapi-test",
		SDKMaxRetries: intPtr(0),
	})

	es, err := be.Open(context.Background(), call, testCandidate("nvidia/llama-3.1-nemotron-nano-8b-v1"))
	if err != nil {
		t.Fatal(err)
	}
	_ = drainStream(t, es)

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(capturedBody, `"chat_template_kwargs"`) {
		t.Fatalf("captured body must contain chat_template_kwargs, body=%s", capturedBody)
	}
}

// --- Responses Tests ---

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

func TestIntegration_responsesNonStream(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{})

	be := nvidia.New(nvidia.Config{
		BaseURL:       srv.URL,
		APIKey:        "nvapi-test",
		SDKMaxRetries: intPtr(0),
	})

	es, err := be.Open(context.Background(), responsesTestCall(nil), testCandidate("nvidia/llama-3.1-nemotron-nano-8b-v1"))
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
		t.Fatal("expected text delta with 'nvidia-ok'")
	}
}

func TestIntegration_responsesNonStreamUsage(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{
		ResponsesNonStreamJSON: `{
  "id": "resp_nvidia_refbackend_usage",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "nvidia/llama-3.1-nemotron-nano-8b-v1",
  "output": [
    {
      "type": "message",
      "id": "msg_out",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "output_text", "text": "nvidia-ok"}
      ]
    }
  ],
  "usage": {
    "input_tokens": 3,
    "output_tokens": 7,
    "total_tokens": 10,
    "output_tokens_details": {"reasoning_tokens": 5}
  }
}`,
	})

	be := nvidia.New(nvidia.Config{
		BaseURL:       srv.URL,
		APIKey:        "nvapi-test",
		SDKMaxRetries: intPtr(0),
	})

	es, err := be.Open(context.Background(), responsesTestCall(nil), testCandidate("nvidia/llama-3.1-nemotron-nano-8b-v1"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)

	var usage lipapi.Event
	found := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventUsageDelta {
			usage = ev
			found = true
		}
	}
	if !found {
		t.Fatal("expected usage delta event")
	}
	if usage.ReasoningTokens != 5 {
		t.Fatalf("ReasoningTokens = %d, want 5", usage.ReasoningTokens)
	}
	if usage.TotalTokens != 10 {
		t.Fatalf("TotalTokens = %d, want 10", usage.TotalTokens)
	}
}

// --- Transport Caps ---

func TestIntegration_transportCaps_bothOperationsBothModes(t *testing.T) {
	t.Parallel()
	be := nvidia.New(nvidia.Config{BaseURL: "https://integrate.api.nvidia.com/v1", APIKey: "nvapi-test"})
	caps := be.TransportCaps
	if !caps.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeStreaming) {
		t.Fatal("expected chat completions streaming support")
	}
	if !caps.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeNonStreaming) {
		t.Fatal("expected chat completions non-streaming support")
	}
	if !caps.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeStreaming) {
		t.Fatal("expected responses streaming support")
	}
	if !caps.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeNonStreaming) {
		t.Fatal("expected responses non-streaming support")
	}
}

// --- Credential Handling ---

func TestIntegration_authFailureRotatesCredential(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{
		ForcedHTTPStatus: http.StatusUnauthorized,
	})

	call := testCall(nil)
	be := nvidia.New(nvidia.Config{
		BaseURL:       srv.URL,
		APIKeys:       []string{"bad-key"},
		SDKMaxRetries: intPtr(0),
	})

	_, err := be.Open(context.Background(), call, testCandidate("nvidia/llama-3.1-nemotron-nano-8b-v1"))
	if err == nil {
		t.Fatal("expected error for auth failure")
	}
}

func TestIntegration_rateLimitClassification(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{
		ForcedHTTPStatus: http.StatusTooManyRequests,
		ForcedRetryAfter: "60",
	})

	call := testCall(nil)
	be := nvidia.New(nvidia.Config{
		BaseURL:       srv.URL,
		APIKeys:       []string{"key-1"},
		SDKMaxRetries: intPtr(0),
	})

	_, err := be.Open(context.Background(), call, testCandidate("nvidia/llama-3.1-nemotron-nano-8b-v1"))
	if err == nil {
		t.Fatal("expected error for rate limit")
	}
}

func TestIntegration_multiKey401ThenSuccess(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	callCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		n := callCount
		mu.Unlock()

		auth := r.Header.Get("Authorization")
		if n == 1 && strings.Contains(auth, "bad-key") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"incorrect api key","type":"invalid_request_error","code":"invalid_api_key"}}`))
			return
		}
		refbackend.NewHandler(refbackend.Config{AllowMissingBearer: true}).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	call := testCall(nil)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	be := nvidia.New(nvidia.Config{
		BaseURL:       srv.URL,
		APIKeys:       []string{"bad-key", "good-key"},
		SDKMaxRetries: intPtr(0),
	})

	es, err := be.Open(context.Background(), call, testCandidate("nvidia/llama-3.1-nemotron-nano-8b-v1"))
	if err != nil {
		t.Fatalf("expected success with second key, got: %v", err)
	}
	events := drainStream(t, es)
	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected text delta from fallback key")
	}
}

func TestIntegration_multiKey429ThenSuccess(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	callCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		n := callCount
		mu.Unlock()

		auth := r.Header.Get("Authorization")
		if n == 1 && strings.Contains(auth, "limited-key") {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded","type":"requests","code":"rate_limit_exceeded"}}`))
			return
		}
		refbackend.NewHandler(refbackend.Config{AllowMissingBearer: true}).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	call := testCall(nil)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	be := nvidia.New(nvidia.Config{
		BaseURL:       srv.URL,
		APIKeys:       []string{"limited-key", "good-key"},
		SDKMaxRetries: intPtr(0),
	})

	es, err := be.Open(context.Background(), call, testCandidate("nvidia/llama-3.1-nemotron-nano-8b-v1"))
	if err != nil {
		t.Fatalf("expected success with second key, got: %v", err)
	}
	events := drainStream(t, es)
	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected text delta from fallback key")
	}
}

// --- Flavor Selection ---

func TestIntegration_flavorSelection_defaultIsChat(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{})

	call := testCall(nil)
	be := nvidia.New(nvidia.Config{
		BaseURL:       srv.URL,
		APIKey:        "nvapi-test",
		SDKMaxRetries: intPtr(0),
	})
	es, err := be.Open(context.Background(), call, testCandidate("nvidia/llama-3.1-nemotron-nano-8b-v1"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)
	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected text delta from chat endpoint")
	}
}

func TestIntegration_flavorSelection_responsesWhenExtensionSet(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{})

	ext := map[string]json.RawMessage{
		openrouterwire.ExtUpstreamFlavor: json.RawMessage(`"responses"`),
	}
	call := testCall(ext)
	be := nvidia.New(nvidia.Config{
		BaseURL:       srv.URL,
		APIKey:        "nvapi-test",
		SDKMaxRetries: intPtr(0),
	})
	es, err := be.Open(context.Background(), call, testCandidate("nvidia/llama-3.1-nemotron-nano-8b-v1"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)
	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected text delta from responses endpoint")
	}
}
