package huggingface_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/huggingface"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func testCall() lipapi.Call {
	return lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
		}},
	}
}

func testCandidate(model string) routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Model: model}}
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

func hasTextDelta(events []lipapi.Event, want string) bool {
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, want) {
			return true
		}
	}
	return false
}

func TestIntegration_chatCompletionsNonStream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var body struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Model != "openai/gpt-oss-120b:fastest" {
			t.Fatalf("model = %q", body.Model)
		}
		if body.Stream {
			t.Fatal("non-streaming request set stream=true")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-hf","object":"chat.completion","created":1715620000,"model":"openai/gpt-oss-120b:fastest","choices":[{"index":0,"message":{"role":"assistant","content":"hf-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	t.Cleanup(srv.Close)

	call := testCall()
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	be := huggingface.New(huggingface.Config{BaseURL: srv.URL + "/v1", APIKey: "hf-test", HTTPClient: srv.Client()})
	es, err := be.Open(context.Background(), call, testCandidate("openai/gpt-oss-120b:fastest"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasTextDelta(drainStream(t, es), "hf-ok") {
		t.Fatal("expected hf-ok text delta")
	}
}

func TestIntegration_chatCompletionsStreaming(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var body struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !body.Stream {
			t.Fatal("streaming request did not set stream=true")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-hf\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"openai/gpt-oss-120b:fastest\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hf-stream-ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)

	call := testCall()
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  lipapi.DeliveryModeStreaming,
		TransportMode: lipapi.TransportModeStreaming,
	}
	be := huggingface.New(huggingface.Config{BaseURL: srv.URL + "/v1", APIKey: "hf-test", HTTPClient: srv.Client()})
	es, err := be.Open(context.Background(), call, testCandidate("openai/gpt-oss-120b:fastest"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasTextDelta(drainStream(t, es), "hf-stream-ok") {
		t.Fatal("expected hf-stream-ok text delta")
	}
}
