package geminigenerate_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/geminigenerate"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func requireReasoningReplayUnsupported(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("RED: Gemini must fail closed for reasoning replay")
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "must-not-drop") || strings.Contains(msg, "must-not-convert") {
		t.Fatalf("Gemini must not convert reasoning into text payload: %v", err)
	}
	if !strings.Contains(msg, "reasoning replay") && !strings.Contains(msg, "reasoning_replay") {
		t.Fatalf("RED: Gemini unsupported error must classify reasoning-replay incompatibility (want substring %q or %q), got %v", "reasoning replay", "reasoning_replay", err)
	}
}

func TestStreamParamsForCall_assistantReasoningReplayUnsupported_RED(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "gemini-reasoning-unsupported",
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}},
			{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					{
						Kind: lipapi.PartReasoning,
						Reasoning: &lipapi.ReasoningPart{
							Dialect: lipapi.ReasoningDialectOpenAIChatTextV1,
							Text:    "must-not-drop",
						},
					},
					lipapi.TextPart("answer"),
				},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	_, err := geminigenerate.StreamParamsForCall(&call, cand)
	requireReasoningReplayUnsupported(t, err)
}

func TestStreamParamsForCall_noPositiveReasoningReplayGolden_RED(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleAssistant,
			Parts: []lipapi.Part{{Kind: lipapi.PartReasoning, Reasoning: &lipapi.ReasoningPart{Dialect: lipapi.ReasoningDialectOpenAIChatTextV1, Text: "x"}}},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	_, err := geminigenerate.StreamParamsForCall(&call, cand)
	requireReasoningReplayUnsupported(t, err)
}
