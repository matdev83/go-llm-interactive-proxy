package reasoningpreservation_test

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestPhase3_itemAuthorityIsNotMutatedByMessagePlacementRestore(t *testing.T) {
	t.Parallel()
	reasoning := reasoningPart(lipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"r1","type":"reasoning","summary":[],"encrypted_content":"stored"}`))
	callPart := jsonPart(`{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{}"}`)
	callPart.ToolCallID = "call-1"
	callPart.ToolName = "lookup"
	call := lipapi.Call{Items: []lipapi.Item{
		{Kind: lipapi.ItemKindToolCall, ToolCall: &lipapi.ToolCallItem{CallID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{}`)}},
	}}
	before := cloneCall(t, call)
	got, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
		Action:        reasoningpreservation.ActionRestore,
		Call:          &call,
		Artifacts:     []reasoningpreservation.TurnArtifact{turnArtifact("stored", anchorFor(t, callPart), placedReasoning(0, reasoning))},
		ReplaySupport: exactResponsesSupport(),
		Eligible:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mutated || got.RestoredCount != 0 || len(call.Items) != len(before.Items) || call.Items[0].Kind != before.Items[0].Kind {
		t.Fatalf("item authority must not be rewritten by message placement restore: result=%+v items=%+v", got, call.Items)
	}
}

func TestPhase3_legacyTrajectoryMutationCasesNeverGuess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		call        lipapi.Call
		artifacts   []reasoningpreservation.TurnArtifact
		wantOutcome reasoningpreservation.SafeOutcome
	}{
		{
			name:        "edited trajectory is unmatched",
			call:        lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart("edited")}}}},
			artifacts:   []reasoningpreservation.TurnArtifact{turnArtifact("original", anchorFor(t, lipapi.TextPart("original")), placedReasoning(0, reasoningPart(lipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"r-edit","type":"reasoning","summary":[]}`))))},
			wantOutcome: reasoningpreservation.OutcomeUnmatched,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
				Action:        reasoningpreservation.ActionRestore,
				Call:          &tc.call,
				Artifacts:     tc.artifacts,
				ReplaySupport: exactResponsesSupport(),
				Eligible:      true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Mutated || len(got.Outcomes) != 1 || got.Outcomes[0] != tc.wantOutcome {
				t.Fatalf("trajectory decision = %+v", got)
			}
		})
	}
}

func TestPhase3_itemAuthorityClientReasoningIsNeverDuplicated(t *testing.T) {
	t.Parallel()
	reasoning := reasoningPart(lipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"r1","type":"reasoning","summary":[],"encrypted_content":"client"}`))
	call := lipapi.Call{Items: []lipapi.Item{
		{Kind: lipapi.ItemKindReasoning, Reasoning: &lipapi.ReasoningItem{Reasoning: reasoning.Reasoning}},
		{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleAssistant, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "done"}}},
	}}
	anchor := anchorFor(t, lipapi.TextPart("done"))
	got, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
		Action:        reasoningpreservation.ActionRestore,
		Call:          &call,
		Artifacts:     []reasoningpreservation.TurnArtifact{turnArtifact("same", anchor, placedReasoning(0, reasoning))},
		ReplaySupport: exactResponsesSupport(),
		Eligible:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mutated || got.RestoredCount != 0 || len(call.Items) != 2 {
		t.Fatalf("client reasoning must remain exact and singular: result=%+v items=%+v", got, call.Items)
	}
}

func TestPhase3_restoreLegacyMultipleReasoningAroundFunctionCallAndToolOutput(t *testing.T) {
	t.Parallel()
	r1 := reasoningPart(lipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"r1","type":"reasoning","summary":[],"encrypted_content":"one"}`))
	r2 := reasoningPart(lipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"r2","type":"reasoning","summary":[],"encrypted_content":"two"}`))
	text := lipapi.TextPart("before call")
	callPart := jsonPart(`{}`)
	callPart.ToolCallID = "call-1"
	callPart.ToolName = "lookup"
	toolOutput := lipapi.Part{Kind: lipapi.PartToolResult, ToolCallID: "call-1", Content: json.RawMessage(`"result"`)}
	call := lipapi.Call{Messages: []lipapi.Message{
		{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{text, callPart}},
		{Role: lipapi.RoleTool, Parts: []lipapi.Part{toolOutput}},
		{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart("after tool")}},
	}}
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("first", anchorFor(t, text, callPart), placedReasoning(0, r1), placedReasoning(1, r2)),
		turnArtifact("second", anchorFor(t, lipapi.TextPart("after tool")), placedReasoning(0, reasoningPart(lipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"r3","type":"reasoning","summary":[],"encrypted_content":"three"}`)))),
	}
	got, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
		Action:        reasoningpreservation.ActionRestore,
		Call:          &call,
		Artifacts:     artifacts,
		ReplaySupport: exactResponsesSupport(),
		Eligible:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.RestoredCount != 2 {
		t.Fatalf("restore result = %+v", got)
	}
	parts := call.Messages[0].Parts
	if len(parts) != 4 || parts[0].Kind != lipapi.PartReasoning || parts[1].Kind != lipapi.PartText || parts[2].Kind != lipapi.PartReasoning || parts[3].Kind != lipapi.PartJSON {
		t.Fatalf("reasoning/function-call order = %+v", parts)
	}
	if len(call.Messages[2].Parts) != 2 || call.Messages[2].Parts[0].Kind != lipapi.PartReasoning {
		t.Fatalf("later reasoning after tool output = %+v", call.Messages[2].Parts)
	}
}
