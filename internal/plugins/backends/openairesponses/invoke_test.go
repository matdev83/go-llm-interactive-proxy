package openairesponses_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
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
		Route: lipapi.RouteIntent{Selector: "openai-responses:gpt-4o-mini"},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "openai-responses", Model: "gpt-4o-mini"},
		Key:     "openai-responses:gpt-4o-mini",
	}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Model) != "gpt-4o-mini" {
		t.Fatalf("model: %s", p.Model)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"model":"gpt-4o-mini"`) {
		t.Fatalf("marshaled params: %s", raw)
	}
}

func TestParamsForCall_modelFromExtensions(t *testing.T) {
	t.Parallel()
	rawModel, _ := json.Marshal("gpt-4o-mini")
	call := lipapi.Call{
		ID: "t2",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("x")},
		}},
		Extensions: map[string]json.RawMessage{"openairesponses.model": rawModel},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-responses"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Model) != "gpt-4o-mini" {
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
				func() lipapi.Part {
					p := lipapi.Part{Kind: lipapi.PartImageRef, ImageRef: "data:image/png;base64,AAA"}
					return p
				}(),
				lipapi.FilePart("data:application/pdf;base64,QUFB", "application/pdf", "minimal.pdf"),
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "input_image") || !strings.Contains(s, "input_file") {
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
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("empty text part should be skipped, not error: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, "input_text") {
		t.Fatalf("empty text part should not appear in output: %s", s)
	}
	if !strings.Contains(s, "input_image") {
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
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	_, err := backend.ParamsForCall(&call, cand)
	if err == nil {
		t.Fatal("expected error for non-data URL file ref")
	}
	if !strings.Contains(err.Error(), "data URL") {
		t.Fatalf("unexpected error: %v", err)
	}
}
