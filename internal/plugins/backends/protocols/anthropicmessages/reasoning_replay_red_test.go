package anthropicmessages_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/anthropicmessages"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func thinkingPart(text, signature string) lipapi.Part {
	return lipapi.Part{
		Kind: lipapi.PartReasoning,
		Reasoning: &lipapi.ReasoningPart{
			Dialect:   lipapi.ReasoningDialectAnthropicThinkingV1,
			Text:      text,
			Signature: signature,
		},
	}
}

func redactedPart(data string) lipapi.Part {
	opaque, _ := json.Marshal(map[string]string{"type": "redacted_thinking", "data": data})
	return lipapi.Part{
		Kind: lipapi.PartReasoning,
		Reasoning: &lipapi.ReasoningPart{
			Dialect: lipapi.ReasoningDialectAnthropicRedactedThinkingV1,
			Opaque:  opaque,
		},
	}
}

func TestParamsForCall_assistantThinkingAndRedactedBlocks_RED(t *testing.T) {
	t.Parallel()
	toolOpaque := json.RawMessage(`{"q":"x"}`)
	call := lipapi.Call{
		ID: "anthropic-reasoning-replay",
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}},
			{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					thinkingPart("plan", "sig-a"),
					lipapi.TextPart("between"),
					{Kind: lipapi.PartJSON, ToolCallID: "toolu_1", ToolName: "lookup", Content: toolOpaque},
					redactedPart("opaque-blob"),
					lipapi.TextPart("answer"),
				},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	p, err := anthropicmessages.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("RED: ParamsForCall must encode thinking/redacted_thinking blocks: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"type":"thinking"`) || !strings.Contains(s, `"thinking":"plan"`) || !strings.Contains(s, `"signature":"sig-a"`) {
		t.Fatalf("RED: must encode signed thinking block, got %s", s)
	}
	if !strings.Contains(s, `"type":"tool_use"`) || !strings.Contains(s, `"id":"toolu_1"`) {
		t.Fatalf("RED: must preserve ordered tool_use among thinking blocks, got %s", s)
	}
	if !strings.Contains(s, `"type":"redacted_thinking"`) || !strings.Contains(s, `"data":"opaque-blob"`) {
		t.Fatalf("RED: must encode redacted_thinking block, got %s", s)
	}
}

func TestParamsForCall_assistantThinkingEmptySignatureNotFabricated_RED(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "anthropic-empty-signature",
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}},
			{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					thinkingPart("plan", ""),
					lipapi.TextPart("answer"),
				},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	p, err := anthropicmessages.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("RED: ParamsForCall must encode thinking with empty signature: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"type":"thinking"`) {
		t.Fatalf("RED: must encode thinking block, got %s", s)
	}
	if strings.Contains(s, `"signature":"`) && !strings.Contains(s, `"signature":""`) {
		t.Fatalf("RED: empty signature must stay empty (no fabrication), got %s", s)
	}
}
