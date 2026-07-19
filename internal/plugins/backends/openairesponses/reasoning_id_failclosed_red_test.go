package openairesponses_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestParamsForCall_reasoningMissingIDFailsClosed_RED(t *testing.T) {
	t.Parallel()
	opaque := json.RawMessage(`{"summary":[{"type":"summary_text","text":"s"}],"encrypted_content":"enc"}`)
	call := lipapi.Call{
		ID: "responses-missing-id",
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}},
			{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					{
						Kind: lipapi.PartReasoning,
						Reasoning: &lipapi.ReasoningPart{
							Dialect: lipapi.ReasoningDialectOpenAIResponsesItemV1,
							Opaque:  opaque,
						},
					},
					lipapi.TextPart("answer"),
				},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	_, err := backend.ParamsForCall(&call, cand)
	if err == nil {
		t.Fatal("RED: missing reasoning item id must fail closed")
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "rs_replay") {
		t.Fatalf("RED: must not fabricate rs_replay id, got %v", err)
	}
	if !strings.Contains(msg, "id") {
		t.Fatalf("RED: error must mention missing id, got %v", err)
	}
}
