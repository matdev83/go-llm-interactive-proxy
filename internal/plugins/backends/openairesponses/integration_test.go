package openairesponses_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

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
		Key:     "openai-responses:gpt-4o-mini",
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

func TestIntegration_refbackendNonStreamUsage(t *testing.T) {
	t.Parallel()
	const inner = `{"id":"resp_usage","object":"response","created_at":1715620000,"status":"completed","model":"gpt-4o-mini","usage":{"input_tokens":3,"output_tokens":7,"total_tokens":10,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}},"output":[{"type":"message","id":"msg_out","status":"completed","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`
	wrapped := `{"type":"response.completed","sequence_number":1,"response":` + inner + `}`
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		StreamSSE: "event: response.completed\ndata: " + wrapped + "\n\ndata: [DONE]\n\n",
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	call := lipapi.Call{
		ID: "int2",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("ping")},
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
	if col.InputTokens != 3 || col.OutputTokens != 7 {
		t.Fatalf("usage: in=%d out=%d", col.InputTokens, col.OutputTokens)
	}
	if col.Text.String() != "done" {
		t.Fatalf("text: %q", col.Text.String())
	}
}

func TestIntegration_refbackendMultimodalRequestBody(t *testing.T) {
	t.Parallel()
	var captured string
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestBody: func(b []byte) { captured = string(b) },
		NonStreamJSON: `{"id":"x","object":"response","created_at":1,"status":"completed","model":"gpt-4o-mini","output":[{"type":"message","id":"m","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`,
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
	// Non-streaming path from SDK still POSTs; refbackend switches on "stream":true in body.
	// ParamsForCall does not set stream — NewStreaming adds stream:true in SDK.
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), es); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(captured, "input_image") || !strings.Contains(captured, "input_file") {
		t.Fatalf("expected multimodal request markers, got: %s", captured)
	}
}

func TestIntegration_refbackendToolCallStream(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"q":{"type":"integer"}}}`)
	const toolStreamSSE = "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_int_t\",\"call_id\":\"call_fc\",\"name\":\"get_weather\",\"status\":\"in_progress\"}}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":1,\"item_id\":\"fc_int_t\",\"output_index\":0,\"delta\":\"{\\\"q\\\":\"}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":2,\"item_id\":\"fc_int_t\",\"output_index\":0,\"delta\":\"1}\"}\n\n" +
		"event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"sequence_number\":3,\"item_id\":\"fc_int_t\",\"output_index\":0,\"name\":\"get_weather\",\"arguments\":\"{\\\"q\\\":1}\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":4,\"response\":{\"id\":\"r_tool\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-4o-mini\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc_int_t\",\"name\":\"get_weather\",\"arguments\":\"{\\\"q\\\":1}\"}]}}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{StreamSSE: toolStreamSSE}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	call := lipapi.Call{
		ID: "tool-int",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("weather?")},
		}},
		Tools: []lipapi.ToolDef{{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters:  schema,
		}},
		ToolChoice: lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired, Name: "get_weather"},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	tcs := col.OrderedToolCalls()
	if len(tcs) != 1 {
		t.Fatalf("tool calls: %+v", tcs)
	}
	if tcs[0].Name != "get_weather" || tcs[0].ID != "fc_int_t" {
		t.Fatalf("tool summary: %+v", tcs[0])
	}
	if tcs[0].Arguments != `{"q":1}` {
		t.Fatalf("arguments: %q", tcs[0].Arguments)
	}
}
