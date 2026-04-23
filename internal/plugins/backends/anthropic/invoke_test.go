package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestParamsForCall_textOnly(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "t1",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
		Route: lipapi.RouteIntent{Selector: "anthropic:claude-3-5-haiku-20241022"},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "anthropic", Model: "claude-3-5-haiku-20241022"},
		Key:     "anthropic:claude-3-5-haiku-20241022",
	}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Model) != "claude-3-5-haiku-20241022" {
		t.Fatalf("model: %s", p.Model)
	}
	if p.MaxTokens != 4096 {
		t.Fatalf("default max_tokens: %d", p.MaxTokens)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"model":"claude-3-5-haiku-20241022"`) || !strings.Contains(s, `"max_tokens":4096`) {
		t.Fatalf("marshaled params: %s", s)
	}
}

func TestParamsForCall_modelFromExtensions(t *testing.T) {
	t.Parallel()
	rawModel, _ := json.Marshal("claude-3-5-haiku-20241022")
	call := lipapi.Call{
		ID: "t2",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("x")},
		}},
		Extensions: map[string]json.RawMessage{"anthropic.model": rawModel},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "anthropic"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Model) != "claude-3-5-haiku-20241022" {
		t.Fatalf("model: %s", p.Model)
	}
}

func TestParamsForCall_multimodalParts(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "t3",
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.TextPart("describe"),
				{Kind: lipapi.PartImageRef, ImageRef: "data:image/png;base64,AAA"},
				lipapi.FilePart("data:application/pdf;base64,QUFB", "application/pdf", "minimal.pdf"),
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"type":"image"`) || !strings.Contains(s, `"type":"document"`) {
		t.Fatalf("expected multimodal markers, got: %s", s)
	}
}

func TestParamsForCall_emptyTextPartSkipped(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "empty-text",
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.TextPart(""),
				{Kind: lipapi.PartImageRef, ImageRef: "data:image/png;base64,AAA"},
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("empty text part should be skipped, not error: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, `"text":""`) {
		t.Fatalf("empty text part should not appear as empty text block: %s", s)
	}
	if !strings.Contains(s, `"type":"image"`) {
		t.Fatalf("image part should be present: %s", s)
	}
}

func TestParamsForCall_fileRefNonDataURL_rejected(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "bad-file-ref",
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.TextPart("x"),
				lipapi.FilePart("https://example.com/file.pdf", "application/pdf", "file.pdf"),
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	_, err := backend.ParamsForCall(&call, cand)
	if err == nil {
		t.Fatal("expected error for non-data URL file ref")
	}
	if !strings.Contains(err.Error(), "data URL") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParamsForCall_toolsAndParallel(t *testing.T) {
	t.Parallel()
	parallel := false
	call := lipapi.Call{
		ID: "tools",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("use a tool")},
		}},
		Tools: []lipapi.ToolDef{{
			Name:        "get_weather",
			Description: "Weather lookup",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		}},
		ToolChoice: lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto},
		Options:    lipapi.GenerationOptions{ParallelToolCalls: &parallel},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"name":"get_weather"`) {
		t.Fatalf("expected tool name: %s", s)
	}
	if !strings.Contains(s, `"disable_parallel_tool_use":true`) {
		t.Fatalf("expected disable_parallel_tool_use when ParallelToolCalls is false: %s", s)
	}
	_ = p
}

func TestParamsForCall_systemInstructions(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "sys",
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("You are concise.")},
		}},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "You are concise.") {
		t.Fatalf("system prompt missing: %s", raw)
	}
}

func TestUpstreamError_returnsAPIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`))
	}))
	t.Cleanup(srv.Close)

	cli := anthropic.NewClient(
		option.WithBaseURL(srv.URL),
		option.WithAPIKey(testkit.SyntheticAnthropicAPIKey),
	)
	_, err := cli.Messages.New(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.Model("claude-3-5-haiku-20241022"),
		MaxTokens: 8,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("x")),
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *anthropic.Error, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", apiErr.StatusCode)
	}
}
