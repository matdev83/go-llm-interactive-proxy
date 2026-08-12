package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func FuzzNativeHistoryBuilder_BoundedAndContentSafe(f *testing.F) {
	f.Add(`{"id":"r1","type":"reasoning","summary":[],"encrypted_content":"opaque"}`, uint8(0), "call-1", "lookup")
	f.Add(`not-json`, uint8(1), "", "")
	f.Add(`{"type":"reasoning","summary":[]}`, uint8(2), "call-2", "lookup")
	f.Add(`{"id":"r2","type":"reasoning","summary":[],"encrypted_content":"`+strings.Repeat("x", 256)+`"}`, uint8(3), "call-3", "lookup")

	f.Fuzz(func(t *testing.T, raw string, mutation uint8, callID, toolName string) {
		if len(raw) > lipapi.MaxReasoningOpaqueBytes*2 {
			raw = raw[:lipapi.MaxReasoningOpaqueBytes*2]
		}
		callID = boundedFuzzString(callID, "call-1", lipapi.MaxRefStringBytes)
		toolName = boundedFuzzString(toolName, "lookup", lipapi.MaxToolNameBytes)
		call := nativeHistoryFuzzCall(raw, mutation, callID, toolName)

		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("native history builder panicked: %v", recovered)
			}
		}()
		history, err := buildNativeHistory(call)
		if err != nil {
			assertNativeHistoryErrorSafe(t, err, raw)
			return
		}
		if len(history.Items) > lipapi.MaxItems || len(history.Fingerprints) != len(history.Items) {
			t.Fatalf("unbounded history: items=%d fingerprints=%d", len(history.Items), len(history.Fingerprints))
		}
		for _, fingerprint := range history.Fingerprints {
			if len(fingerprint) != 64 {
				t.Fatalf("fingerprint length = %d", len(fingerprint))
			}
		}
	})
}

func FuzzNativeHistoryBuilder_BoundaryMutation(f *testing.F) {
	f.Add(uint8(0), uint8(0))
	f.Add(uint8(1), uint8(2))
	f.Add(uint8(2), uint8(3))

	f.Fuzz(func(t *testing.T, mutation, discriminator uint8) {
		call := nativeHistoryTrajectoryCall([]lipapi.ToolDef{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}})
		switch mutation % 5 {
		case 0:
			call.Messages[1], call.Messages[2] = call.Messages[2], call.Messages[1]
		case 1:
			call.Messages[2].Parts[0].ToolCallID = "missing"
		case 2:
			call.Messages[1].Parts[1].Content = []byte(`{"type":"function_call","call_id":"` + string(rune('a'+discriminator%26)) + `","name":"lookup","arguments":{}}`)
		case 3:
			call.Messages[1].Parts[0].Reasoning.Dialect = lipapi.ReasoningDialectOpenAIChatTextV1
		case 4:
			call.Messages[1].Parts[1].Content = []byte(`{"type":"future_item","payload":"opaque"}`)
		}

		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("boundary mutation panicked: %v", recovered)
			}
		}()
		history, err := buildNativeHistory(call)
		if err != nil {
			assertNativeHistoryErrorSafe(t, err, "opaque")
			return
		}
		if len(history.SafeSplitIndices()) == 0 {
			t.Fatal("valid history has no safe split boundary")
		}
	})
}

func nativeHistoryFuzzCall(raw string, mutation uint8, callID, toolName string) *lipapi.Call {
	if callID == "" {
		callID = "call-1"
	}
	if toolName == "" {
		toolName = "lookup"
	}
	if mutation%3 == 0 {
		return &lipapi.Call{Items: []lipapi.Item{{
			Kind:   lipapi.ItemKindReasoning,
			Status: lipapi.ItemStatusCompleted,
			Reasoning: &lipapi.ReasoningItem{Reasoning: &lipapi.ReasoningPart{
				Dialect: lipapi.ReasoningDialectOpenAIResponsesItemV1,
				Opaque:  json.RawMessage(raw),
			}},
		}}}
	}
	return &lipapi.Call{
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("prompt")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{
				{Kind: lipapi.PartReasoning, Reasoning: &lipapi.ReasoningPart{Dialect: lipapi.ReasoningDialectOpenAIResponsesItemV1, Opaque: json.RawMessage(raw)}},
				{Kind: lipapi.PartJSON, ToolCallID: callID, ToolName: toolName, Content: json.RawMessage(`{"type":"function_call","call_id":"` + callID + `","name":"` + toolName + `","arguments":{}}`)},
			}},
			{Role: lipapi.RoleTool, Parts: []lipapi.Part{{Kind: lipapi.PartToolResult, ToolCallID: callID, Content: json.RawMessage(`{"output":"result"}`)}}},
		},
	}
}

func boundedFuzzString(value, fallback string, maxLen int) string {
	if value == "" {
		return fallback
	}
	if len(value) > maxLen {
		return value[:maxLen]
	}
	return value
}

func assertNativeHistoryErrorSafe(t *testing.T, err error, payload string) {
	t.Helper()
	if err == nil || strings.TrimSpace(err.Error()) == "" {
		t.Fatal("expected a stable content-safe error")
	}
	if len(err.Error()) > 256 {
		t.Fatalf("error is not bounded: %d bytes", len(err.Error()))
	}
	// Very short fuzz values commonly occur inside fixed error category names;
	// only assert non-leakage for a distinctive payload.
	if len(payload) >= 16 && len(payload) < 256 && strings.Contains(err.Error(), payload) {
		t.Fatalf("payload leaked in error: %v", err)
	}
}
