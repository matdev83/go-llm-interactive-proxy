//go:build integration

package openrouter_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openrouter"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openrouter"
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
	be := openrouter.New(openrouter.Config{
		BaseURL:       srv.URL,
		APIKey:        "sk-test",
		SDKMaxRetries: new(int),
	})

	es, err := be.Open(context.Background(), call, testCandidate("openai/gpt-4o-mini"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)

	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, "or-ok") {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected text delta with 'or-ok'")
	}

	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(capturedBody, `"stream":true`) {
		t.Fatalf("non-streaming must not set stream:true, body=%s", capturedBody)
	}
}

func TestIntegration_chatCompletionsStreamingTransportMode(t *testing.T) {
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
	be := openrouter.New(openrouter.Config{
		BaseURL:       srv.URL,
		APIKey:        "sk-test",
		SDKMaxRetries: new(int),
	})

	es, err := be.Open(context.Background(), call, testCandidate("openai/gpt-4o-mini"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)

	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, "or-stream-ok") {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected text delta with 'or-stream-ok'")
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
  "id": "gen-or-refbackend-usage",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "openai/gpt-4o-mini",
  "choices": [{"index":0,"message":{"role":"assistant","content":"or-ok"},"finish_reason":"stop"}],
  "usage": {
    "prompt_tokens": 3,
    "completion_tokens": 7,
    "total_tokens": 10,
    "completion_tokens_details": {"reasoning_tokens": 5},
    "cost": 0.00014
  }
}`,
	})

	call := testCall(nil)
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	be := openrouter.New(openrouter.Config{
		BaseURL:       srv.URL,
		APIKey:        "sk-test",
		SDKMaxRetries: new(int),
	})

	es, err := be.Open(context.Background(), call, testCandidate("openai/gpt-4o-mini"))
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
	if usage.RawUsageJSON == "" {
		t.Fatal("expected RawUsageJSON")
	}
	if !strings.Contains(usage.RawUsageJSON, "reasoning_tokens") {
		t.Fatalf("RawUsageJSON missing reasoning_tokens: %s", usage.RawUsageJSON)
	}
}

func TestIntegration_transportCaps_chatCompletionsBothModes(t *testing.T) {
	t.Parallel()
	be := openrouter.New(openrouter.Config{BaseURL: "https://openrouter.ai/api/v1", APIKey: "sk-test"})
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
	var mu sync.Mutex
	var capturedBody string

	srv := newRefServer(t, refbackend.Config{
		OnRequestBody: func(b []byte) {
			mu.Lock()
			capturedBody = string(b)
			mu.Unlock()
		},
	})

	be := openrouter.New(openrouter.Config{
		BaseURL:       srv.URL,
		APIKey:        "sk-test",
		SDKMaxRetries: new(int),
	})

	es, err := be.Open(context.Background(), responsesTestCall(nil), testCandidate("openai/gpt-4o-mini"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)

	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, "or-ok") {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected text delta with 'or-ok'")
	}

	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(capturedBody, `"stream":true`) {
		t.Fatalf("non-streaming must not set stream:true, body=%s", capturedBody)
	}
}

func TestIntegration_responsesNonStreamUsage(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{
		ResponsesNonStreamJSON: `{
  "id": "resp_or_refbackend_usage",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "openai/gpt-4o-mini",
  "output": [
    {
      "type": "message",
      "id": "msg_out",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "output_text", "text": "or-ok"}
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

	be := openrouter.New(openrouter.Config{
		BaseURL:       srv.URL,
		APIKey:        "sk-test",
		SDKMaxRetries: new(int),
	})

	es, err := be.Open(context.Background(), responsesTestCall(nil), testCandidate("openai/gpt-4o-mini"))
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
	if usage.RawUsageJSON == "" {
		t.Fatal("expected RawUsageJSON")
	}
	if !strings.Contains(usage.RawUsageJSON, "reasoning_tokens") {
		t.Fatalf("RawUsageJSON missing reasoning_tokens: %s", usage.RawUsageJSON)
	}
}

func TestIntegration_chatCompletionsStream(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedHeaders http.Header
	var capturedBody string

	srv := newRefServer(t, refbackend.Config{
		OnRequestHeaders: func(h http.Header) {
			mu.Lock()
			capturedHeaders = h.Clone()
			mu.Unlock()
		},
		OnRequestBody: func(b []byte) {
			mu.Lock()
			capturedBody = string(b)
			mu.Unlock()
		},
	})

	ext := map[string]json.RawMessage{
		openrouterwire.ExtHTTPReferer: json.RawMessage(`"https://myapp.com"`),
		openrouterwire.ExtTitle:       json.RawMessage(`"MyApp"`),
		openrouterwire.ExtProvider:    json.RawMessage(`{"order":["OpenAI"]}`),
		openrouterwire.ExtRoute:       json.RawMessage(`"fallback"`),
	}
	call := testCall(ext)
	be := openrouter.New(openrouter.Config{
		BaseURL:       srv.URL,
		APIKey:        "sk-test",
		SDKMaxRetries: new(int),
	})

	es, err := be.Open(context.Background(), call, testCandidate("openai/gpt-4o-mini"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(t, es)

	hasText := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, "or-stream-ok") {
			hasText = true
		}
	}
	if !hasText {
		t.Fatal("expected text delta with 'or-stream-ok'")
	}

	mu.Lock()
	defer mu.Unlock()
	if capturedHeaders.Get("Http-Referer") != "https://myapp.com" {
		t.Errorf("HTTP-Referer: %q", capturedHeaders.Get("Http-Referer"))
	}
	if !strings.Contains(capturedBody, `"provider"`) {
		t.Error("expected provider in body")
	}
	if !strings.Contains(capturedBody, `"route"`) {
		t.Error("expected route in body")
	}
}

func TestIntegration_flavorSelection(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{})

	t.Run("default_is_chat", func(t *testing.T) {
		call := testCall(nil)
		be := openrouter.New(openrouter.Config{
			BaseURL:       srv.URL,
			APIKey:        "sk-test",
			SDKMaxRetries: new(int),
		})
		es, err := be.Open(context.Background(), call, testCandidate("openai/gpt-4o-mini"))
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
	})

	t.Run("responses_when_extension_set", func(t *testing.T) {
		ext := map[string]json.RawMessage{
			openrouterwire.ExtUpstreamFlavor: json.RawMessage(`"responses"`),
		}
		call := testCall(ext)
		be := openrouter.New(openrouter.Config{
			BaseURL:       srv.URL,
			APIKey:        "sk-test",
			SDKMaxRetries: new(int),
		})
		es, err := be.Open(context.Background(), call, testCandidate("openai/gpt-4o-mini"))
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
	})

	t.Run("responses_when_openairesponses_model_extension_present", func(t *testing.T) {
		ext := map[string]json.RawMessage{
			"openairesponses.model": json.RawMessage(`"openai/gpt-4o-mini"`),
		}
		call := testCall(ext)
		be := openrouter.New(openrouter.Config{
			BaseURL:       srv.URL,
			APIKey:        "sk-test",
			SDKMaxRetries: new(int),
		})
		es, err := be.Open(context.Background(), call, testCandidate("openai/gpt-4o-mini"))
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
			t.Fatal("expected text delta from responses endpoint via frontend extension fallback")
		}
	})
}

func TestIntegration_headerPrecedence(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedHeaders http.Header

	srv := newRefServer(t, refbackend.Config{
		OnRequestHeaders: func(h http.Header) {
			mu.Lock()
			capturedHeaders = h.Clone()
			mu.Unlock()
		},
	})

	ext := map[string]json.RawMessage{
		openrouterwire.ExtHTTPReferer: json.RawMessage(`"https://override.com"`),
	}
	call := testCall(ext)
	be := openrouter.New(openrouter.Config{
		BaseURL:       srv.URL,
		APIKey:        "sk-test",
		SDKMaxRetries: new(int),
		StaticReferer: "https://default.com",
		StaticTitle:   "DefaultTitle",
	})

	es, err := be.Open(context.Background(), call, testCandidate("openai/gpt-4o-mini"))
	if err != nil {
		t.Fatal(err)
	}
	_ = drainStream(t, es)

	mu.Lock()
	defer mu.Unlock()
	if capturedHeaders.Get("Http-Referer") != "https://override.com" {
		t.Errorf("HTTP-Referer should be per-request override, got %q", capturedHeaders.Get("Http-Referer"))
	}
	if capturedHeaders.Get("X-Title") != "DefaultTitle" {
		t.Errorf("X-Title should be static default, got %q", capturedHeaders.Get("X-Title"))
	}
}

func TestIntegration_authFailureRotatesCredential(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{
		ForcedHTTPStatus: http.StatusUnauthorized,
	})

	call := testCall(nil)
	be := openrouter.New(openrouter.Config{
		BaseURL:       srv.URL,
		APIKeys:       []string{"bad-key"},
		SDKMaxRetries: new(int),
	})

	_, err := be.Open(context.Background(), call, testCandidate("openai/gpt-4o-mini"))
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
	be := openrouter.New(openrouter.Config{
		BaseURL:       srv.URL,
		APIKeys:       []string{"key-1"},
		SDKMaxRetries: new(int),
	})

	_, err := be.Open(context.Background(), call, testCandidate("openai/gpt-4o-mini"))
	if err == nil {
		t.Fatal("expected error for rate limit")
	}
}

func TestIntegration_chatStreamUsage(t *testing.T) {
	t.Parallel()
	srv := newRefServer(t, refbackend.Config{
		ChatStreamSSE: refbackend.ChatStreamWithUsageSSE,
	})

	call := testCall(nil)
	be := openrouter.New(openrouter.Config{
		BaseURL:       srv.URL,
		APIKey:        "sk-test",
		SDKMaxRetries: new(int),
	})

	es, err := be.Open(context.Background(), call, testCandidate("openai/gpt-4o-mini"))
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

func TestIntegration_forwardsOpenRouterExtensionsBroadly(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var capturedHeaders http.Header
	var capturedBody []byte

	srv := newRefServer(t, refbackend.Config{
		OnRequestHeaders: func(h http.Header) {
			mu.Lock()
			capturedHeaders = h.Clone()
			mu.Unlock()
		},
		OnRequestBody: func(b []byte) {
			mu.Lock()
			capturedBody = append([]byte(nil), b...)
			mu.Unlock()
		},
	})

	ext := map[string]json.RawMessage{
		openrouterwire.ExtHTTPReferer:         json.RawMessage(`"https://extensions.example/app"`),
		openrouterwire.ExtTitle:               json.RawMessage(`"ExtensionsApp"`),
		openrouterwire.ExtCategories:          json.RawMessage(`"ai,chat"`),
		openrouterwire.ExtMetadataHeader:      json.RawMessage(`"{\"session\":\"abc\"}"`),
		openrouterwire.ExtProvider:            json.RawMessage(`{"order":["OpenAI"],"allow_fallbacks":false}`),
		openrouterwire.ExtModels:              json.RawMessage(`["openai/gpt-4o-mini","anthropic/claude-3.5-sonnet"]`),
		openrouterwire.ExtRoute:               json.RawMessage(`"fallback"`),
		openrouterwire.ExtPlugins:             json.RawMessage(`[{"id":"web"}]`),
		openrouterwire.ExtPrediction:          json.RawMessage(`{"type":"content","content":"hello"}`),
		openrouterwire.ExtDebug:               json.RawMessage(`true`),
		openrouterwire.ExtServiceTier:         json.RawMessage(`"default"`),
		openrouterwire.ExtSessionID:           json.RawMessage(`"sess-123"`),
		openrouterwire.ExtStopServerToolsWhen: json.RawMessage(`"never"`),
		openrouterwire.ExtTrace:               json.RawMessage(`{"trace_id":"trace-1"}`),
		openrouterwire.ExtInclude:             json.RawMessage(`["reasoning.encrypted_content"]`),
		openrouterwire.ExtUser:                json.RawMessage(`"user-123"`),
		openrouterwire.ExtResponseFormat:      json.RawMessage(`{"type":"json_object"}`),
		openrouterwire.ExtReasoning:           json.RawMessage(`{"effort":"high"}`),
	}

	be := openrouter.New(openrouter.Config{
		BaseURL:       srv.URL,
		APIKey:        "sk-test",
		SDKMaxRetries: new(int),
	})
	es, err := be.Open(context.Background(), testCall(ext), testCandidate("openai/gpt-4o-mini"))
	if err != nil {
		t.Fatal(err)
	}
	_ = drainStream(t, es)

	mu.Lock()
	defer mu.Unlock()

	if capturedHeaders.Get("Http-Referer") != "https://extensions.example/app" {
		t.Errorf("HTTP-Referer: %q", capturedHeaders.Get("Http-Referer"))
	}
	if capturedHeaders.Get("X-Title") != "ExtensionsApp" {
		t.Errorf("X-Title: %q", capturedHeaders.Get("X-Title"))
	}
	if capturedHeaders.Get("X-OpenRouter-Categories") != "ai,chat" {
		t.Errorf("X-OpenRouter-Categories: %q", capturedHeaders.Get("X-OpenRouter-Categories"))
	}
	if capturedHeaders.Get("X-OpenRouter-Metadata") != `{"session":"abc"}` {
		t.Errorf("X-OpenRouter-Metadata: %q", capturedHeaders.Get("X-OpenRouter-Metadata"))
	}

	if len(capturedBody) == 0 {
		t.Fatal("captured request body is empty")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v body=%s", err, string(capturedBody))
	}

	wantForwarded := map[string]string{
		"provider":               `{"order":["OpenAI"],"allow_fallbacks":false}`,
		"models":                 `["openai/gpt-4o-mini","anthropic/claude-3.5-sonnet"]`,
		"route":                  `"fallback"`,
		"plugins":                `[{"id":"web"}]`,
		"prediction":             `{"type":"content","content":"hello"}`,
		"debug":                  `true`,
		"service_tier":           `"default"`,
		"session_id":             `"sess-123"`,
		"stop_server_tools_when": `"never"`,
		"trace":                  `{"trace_id":"trace-1"}`,
		"include":                `["reasoning.encrypted_content"]`,
		"user":                   `"user-123"`,
		"response_format":        `{"type":"json_object"}`,
		"reasoning":              `{"effort":"high"}`,
	}
	for key, want := range wantForwarded {
		got, ok := payload[key]
		if !ok {
			t.Fatalf("missing forwarded field %q in payload: %s", key, string(capturedBody))
		}
		assertJSONRawEqual(t, got, want)
	}
}

func assertJSONRawEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var gotV any
	if err := json.Unmarshal(got, &gotV); err != nil {
		t.Fatalf("unmarshal got %q: %v", string(got), err)
	}
	var wantV any
	if err := json.Unmarshal([]byte(want), &wantV); err != nil {
		t.Fatalf("unmarshal want %q: %v", want, err)
	}
	if !reflect.DeepEqual(gotV, wantV) {
		t.Fatalf("json mismatch: got %s want %s", string(got), want)
	}
}

func newRefServer(t *testing.T, cfg refbackend.Config) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(refbackend.NewHandler(cfg))
	t.Cleanup(srv.Close)
	return srv
}
