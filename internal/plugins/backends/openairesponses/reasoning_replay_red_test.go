package openairesponses_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func responsesReasoningPart(t *testing.T) lipapi.Part {
	t.Helper()
	opaque := json.RawMessage(`{"id":"r1","summary":[{"type":"summary_text","text":"s"}],"content":[{"type":"reasoning_text","text":"c"}],"encrypted_content":"enc"}`)
	return lipapi.Part{
		Kind: lipapi.PartReasoning,
		Reasoning: &lipapi.ReasoningPart{
			Dialect: lipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  opaque,
		},
	}
}

func TestParamsForCall_assistantReasoningResponsesDialect_RED(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "responses-reasoning-replay",
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}},
			{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					responsesReasoningPart(t),
					lipapi.TextPart("answer"),
				},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("RED: ParamsForCall must encode responses reasoning input item: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"type":"reasoning"`) {
		t.Fatalf("RED: params must include reasoning input item, got %s", s)
	}
	if !strings.Contains(s, `"id":"r1"`) || !strings.Contains(s, `"encrypted_content":"enc"`) {
		t.Fatalf("RED: reasoning input item must preserve id and encrypted_content, got %s", s)
	}
}
