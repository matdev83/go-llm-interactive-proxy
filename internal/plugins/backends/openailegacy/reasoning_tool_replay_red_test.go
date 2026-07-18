package openailegacy_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestParamsForCall_assistantReasoningWithToolCall_RED(t *testing.T) {
	t.Parallel()
	toolJSON, err := json.Marshal(map[string]any{
		"id":   "call_1",
		"type": "function",
		"function": map[string]any{
			"name":      "lookup",
			"arguments": `{"q":"x"}`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	call := lipapi.Call{
		ID: "reasoning-tool-replay",
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}},
			{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					{
						Kind: lipapi.PartReasoning,
						Reasoning: &lipapi.ReasoningPart{
							Dialect: lipapi.ReasoningDialectOpenAIChatTextV1,
							Text:    "think",
						},
					},
					lipapi.TextPart("checking"),
					{Kind: lipapi.PartJSON, ToolCallID: "call_1", ToolName: "lookup", Content: []byte(`{"q":"x"}`)},
				},
			},
			{
				Role: lipapi.RoleTool,
				Parts: []lipapi.Part{{
					Kind:       lipapi.PartToolResult,
					ToolCallID: "call_1",
					Content:    []byte(`{"ok":true}`),
				}},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "moonshot-v1-8k"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("RED: ParamsForCall must encode reasoning+tool assistant: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, need := range []string{
		`"reasoning_content":"think"`,
		`"content":"checking"`,
		`"id":"call_1"`,
		`"name":"lookup"`,
		`"arguments":"{\"q\":\"x\"}"`,
	} {
		if !strings.Contains(s, need) {
			t.Fatalf("RED: body missing %s; got %s", need, s)
		}
	}
	_ = toolJSON
}
