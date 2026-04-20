package openailegacy_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaichat"
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
