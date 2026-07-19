package openailegacy_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func reasoningPart(dialect lipapi.ReasoningDialect, text string) lipapi.Part {
	return lipapi.Part{
		Kind: lipapi.PartReasoning,
		Reasoning: &lipapi.ReasoningPart{
			Dialect: dialect,
			Text:    text,
		},
	}
}

func TestParamsForCall_assistantReasoningChatDialect_RED(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "reasoning-replay",
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}},
			{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					reasoningPart(lipapi.ReasoningDialectOpenAIChatTextV1, "think"),
					lipapi.TextPart("answer"),
				},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("RED: ParamsForCall must encode assistant PartReasoning: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"reasoning_content":"think"`) {
		t.Fatalf("RED: chat replay body must include reasoning_content, got %s", s)
	}
}
