package openailegacy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaichat"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestIntegration_refbackendMissingAPIKeyOpenFails(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL + "/v1", APIKey: ""})
	call := lipapi.Call{
		ID: "auth",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	_, err := be.Open(context.Background(), call, cand)
	if err == nil {
		t.Fatal("expected error when API key is missing (no credentials to build pool)")
	}
}

func TestIntegration_refbackendStreamingText(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	call := lipapi.Call{
		ID: "int1",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "gpt-4o-mini"},
		Key:     "openai-legacy:gpt-4o-mini",
	}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(col.Text.String(), "stream-ok") {
		t.Fatalf("text: %q", col.Text.String())
	}
}

func TestIntegration_refbackendStreamUsage(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	streamOptsRaw, _ := json.Marshal(map[string]bool{"include_usage": true})
	be := backend.New(backend.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	call := lipapi.Call{
		ID: "int2",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("ping")},
		}},
		Extensions: map[string]json.RawMessage{
			"openailegacy.stream_options": streamOptsRaw,
		},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "gpt-4o-mini"},
	}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if col.InputTokens != 3 || col.OutputTokens != 7 {
		t.Fatalf("usage: in=%d out=%d", col.InputTokens, col.OutputTokens)
	}
	if !strings.Contains(col.Text.String(), "stream-ok") {
		t.Fatalf("text: %q", col.Text.String())
	}
}

func TestIntegration_refbackendMultimodalRequestBody(t *testing.T) {
	t.Parallel()
	var captured string
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestBody: func(b []byte) { captured = string(b) },
		NonStreamJSON: `{"id":"chatcmpl_x","object":"chat.completion","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	call := lipapi.Call{
		ID: "mm",
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.TextPart("x"),
				{Kind: lipapi.PartImageRef, ImageRef: "https://example.com/i.png"},
				lipapi.FilePart("data:application/pdf;base64,QUFB", "application/pdf", "f.pdf"),
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), es); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(captured, "image_url") || !strings.Contains(captured, `"type":"file"`) {
		t.Fatalf("expected multimodal request markers, got: %s", captured)
	}
}

// Chat Completions SSE with streaming tool_calls (SDK-parseable) and [DONE] terminator.
const refbackendToolCallsStreamSSE = "data: " +
	`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_ab","type":"function","function":{"name":"get_weather"}}]},"finish_reason":null}]}` +
	"\n\n" + "data: " +
	`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\""}}]},"finish_reason":null}]}` +
	"\n\n" + "data: " +
	`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"NYC\"}"}}]},"finish_reason":null}]}` +
	"\n\n" + "data: " +
	`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` +
	"\n\n" + "data: [DONE]\n\n"

func TestIntegration_refbackendToolCallsStream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		StreamSSE: refbackendToolCallsStreamSSE,
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	call := lipapi.Call{
		ID: "tool-int",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("weather?")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "gpt-4o-mini"},
	}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	tools := col.OrderedToolCalls()
	if len(tools) != 1 {
		t.Fatalf("tool calls: %+v", tools)
	}
	if tools[0].ID != "call_ab" || tools[0].Name != "get_weather" || tools[0].Arguments != `{"city":"NYC"}` {
		t.Fatalf("tool summary: %+v", tools[0])
	}
}

func ptrInt(v int) *int { return &v }

func TestIntegration_refbackend429_singleUpstreamHTTPAttemptWhenSDKMaxRetriesZero(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	rb := refbackend.NewHandler(refbackend.Config{
		ForcedHTTPStatus: http.StatusTooManyRequests,
		ForcedRetryAfter: "1",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		rb.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:       srv.URL + "/v1",
		APIKey:        "sk-test",
		HTTPClient:    srv.Client(),
		SDKMaxRetries: ptrInt(0),
	})
	call := lipapi.Call{
		ID: "rl",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "gpt-4o-mini"},
	}
	_, err := be.Open(context.Background(), call, cand)
	if err == nil {
		t.Fatal("expected Open error from 429 refbackend (single credential exhausted)")
	}
	if !lipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("expected recoverable pre-output, got: %v", err)
	}
	if n := reqs.Load(); n != 1 {
		t.Fatalf("upstream HTTP attempts: %d want 1", n)
	}
}

func parseBearerAuthLegacy(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

func TestIntegration_multiKey429ThenSuccessOnSecondCredential(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	ok := refbackend.NewHandler(refbackend.Config{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		switch parseBearerAuthLegacy(r) {
		case "sk-429":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate","type":"requests","code":"rate_limit_exceeded"}}`))
			return
		default:
			ok.ServeHTTP(w, r)
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:       srv.URL + "/v1",
		APIKey:        "sk-429",
		APIKeys:       []string{"sk-429", "sk-ok"},
		HTTPClient:    srv.Client(),
		SDKMaxRetries: ptrInt(0),
	})
	call := lipapi.Call{
		ID: "rot429",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = es.Close() })
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(col.Text.String(), "stream-ok") {
		t.Fatalf("text: %q", col.Text.String())
	}
	if n := reqs.Load(); n != 2 {
		t.Fatalf("upstream HTTP attempts: %d want 2", n)
	}
}

func TestIntegration_multiKey401ThenSuccessOnSecondCredential(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	ok := refbackend.NewHandler(refbackend.Config{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		switch parseBearerAuthLegacy(r) {
		case "sk-bad":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"incorrect api key","type":"invalid_request_error","code":"invalid_api_key"}}`))
			return
		default:
			ok.ServeHTTP(w, r)
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:       srv.URL + "/v1",
		APIKey:        "sk-bad",
		APIKeys:       []string{"sk-bad", "sk-ok"},
		HTTPClient:    srv.Client(),
		SDKMaxRetries: ptrInt(0),
	})
	call := lipapi.Call{
		ID: "rot401",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = es.Close() })
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(col.Text.String(), "stream-ok") {
		t.Fatalf("text: %q", col.Text.String())
	}
	if n := reqs.Load(); n != 2 {
		t.Fatalf("upstream HTTP attempts: %d want 2", n)
	}
}

func TestIntegration_eventOrderUnchangedAfter429Rotation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var reqs atomic.Int32
	ok := refbackend.NewHandler(refbackend.Config{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		if parseBearerAuthLegacy(r) == "sk-429" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate","type":"requests","code":"rate_limit_exceeded"}}`))
			return
		}
		ok.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:       srv.URL + "/v1",
		APIKey:        "sk-429",
		APIKeys:       []string{"sk-429", "sk-ok"},
		HTTPClient:    srv.Client(),
		SDKMaxRetries: ptrInt(0),
	})
	call := lipapi.Call{
		ID: "order",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("order-check")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	es, err := be.Open(ctx, call, cand)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = es.Close() })
	var kinds []lipapi.EventKind
	for {
		ev, rerr := es.Recv(ctx)
		if rerr != nil {
			break
		}
		kinds = append(kinds, ev.Kind)
	}
	if len(kinds) < 3 {
		t.Fatalf("expected several events, got kinds=%v", kinds)
	}
	if kinds[0] != lipapi.EventResponseStarted {
		t.Fatalf("first event: got %v want EventResponseStarted", kinds[0])
	}
}
