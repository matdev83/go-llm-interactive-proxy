package openairesponses_test

import (
	"context"
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
