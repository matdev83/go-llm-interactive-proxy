package openresponses

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestPresenceHelpers(t *testing.T) {
	t.Parallel()

	if b := cloneBytes(nil); b != nil {
		t.Fatalf("expected nil for cloneBytes(nil)")
	}

	if j := ensureNonNullJSON(nil); string(j) != "null" {
		t.Fatalf("expected 'null' for ensureNonNullJSON(nil)")
	}
	if j := ensureNonNullJSON([]byte("null")); string(j) != "null" {
		t.Fatalf("expected 'null' for ensureNonNullJSON('null')")
	}
	if j := ensureNonNullJSON([]byte(`{"a":1}`)); string(j) != `{"a":1}` {
		t.Fatalf("expected JSON byte string, got %s", string(j))
	}

	if m := ensureNonNilMap[string, int](nil); m == nil {
		t.Fatalf("expected non-nil map")
	}
}

func TestEncode_AllItemKinds(t *testing.T) {
	t.Parallel()

	items := []lipapi.Item{
		{
			Kind:   lipapi.ItemKindMessage,
			Role:   lipapi.RoleAssistant,
			Status: lipapi.ItemStatusCompleted,
			Content: []lipapi.ContentPart{
				{Kind: lipapi.ContentPartText, Text: "Assistant response"},
				{Kind: lipapi.ContentPartImageRef, ImageRef: "https://example.com/img.png"},
				{Kind: lipapi.ContentPartRefusal, Refusal: "Refused"},
			},
		},
		{
			Kind:   lipapi.ItemKindItemReference,
			ID:     "msg_ref",
			Status: lipapi.ItemStatusCompleted,
			Reference: &lipapi.ItemReference{
				ID: "msg_ref",
			},
		},
		{
			Kind:   lipapi.ItemKindToolCall,
			ID:     "call_item_1",
			Status: lipapi.ItemStatusCompleted,
			ToolCall: &lipapi.ToolCallItem{
				CallID:    "call_123",
				Name:      "calc",
				Arguments: []byte(`{"expr":"1+1"}`),
			},
		},
		{
			Kind:   lipapi.ItemKindToolResult,
			ID:     "result_item_1",
			Status: lipapi.ItemStatusCompleted,
			ToolResult: &lipapi.ToolResultItem{
				CallID: "call_123",
				Name:   "calc",
				Parts: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "2"},
				},
			},
		},
		{
			Kind:   lipapi.ItemKindReasoning,
			ID:     "reas_item_1",
			Status: lipapi.ItemStatusCompleted,
			Reasoning: &lipapi.ReasoningItem{
				Reasoning: &lipapi.ReasoningPart{
					Text: "Reasoning text",
				},
			},
		},
		{
			Kind:   lipapi.ItemKindCompaction,
			ID:     "comp_item_1",
			Status: lipapi.ItemStatusCompleted,
			Compaction: &lipapi.CompactionItem{
				EncapsulatedID: "enc_1",
				Dialect:        "standard",
				Implementor:    "proxy",
				Opaque:         []byte(`{"key":"val"}`),
			},
		},
		{
			Kind:   lipapi.ItemKindExtension,
			ID:     "ext_item_1",
			Status: lipapi.ItemStatusCompleted,
			Extension: &lipapi.OpaqueExtension{
				Namespace:   "custom",
				Type:        "acme:telemetry",
				Implementor: "acme",
				Direction:   "in",
				Data:        []byte(`{"metric":42}`),
			},
		},
	}

	for i, item := range items {
		wItem, err := EncodeItem(item)
		if err != nil {
			t.Fatalf("EncodeItem failed for item[%d] (%s): %v", i, item.Kind, err)
		}
		if wItem.Type == "" {
			t.Fatalf("expected non-empty type for item[%d]", i)
		}
	}
}

func TestEncode_ToolChoiceModes(t *testing.T) {
	t.Parallel()

	modes := []struct {
		tc      lipapi.ToolChoice
		wantStr string
	}{
		{tc: lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}, wantStr: `"auto"`},
		{tc: lipapi.ToolChoice{Mode: lipapi.ToolChoiceNone}, wantStr: `"none"`},
		{tc: lipapi.ToolChoice{Mode: lipapi.ToolChoiceAny}, wantStr: `"required"`},
		{tc: lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired, Name: "my_fn"}, wantStr: `{"function":{"name":"my_fn"},"type":"function"}`},
		{tc: lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired}, wantStr: `"required"`},
	}

	for _, tt := range modes {
		call := lipapi.Call{
			Items: []lipapi.Item{
				{
					Kind:    lipapi.ItemKindMessage,
					Role:    lipapi.RoleUser,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "hi"}},
				},
			},
			ToolChoice: tt.tc,
		}

		encoded, err := EncodeRequest(call)
		if err != nil {
			t.Fatalf("EncodeRequest failed for tool_choice %v: %v", tt.tc, err)
		}
		if !strings.Contains(string(encoded), tt.wantStr) {
			t.Fatalf("encoded request missing %s: got %s", tt.wantStr, string(encoded))
		}
	}
}

func TestEncode_TopLevelExtensions(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Items: []lipapi.Item{
			{
				Kind:    lipapi.ItemKindMessage,
				Role:    lipapi.RoleUser,
				Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "hi"}},
			},
		},
		Extensions: map[string]json.RawMessage{
			"acme:extension_key": json.RawMessage(`{"custom":true}`),
		},
	}

	encoded, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest failed with extensions: %v", err)
	}

	if !strings.Contains(string(encoded), `"acme:extension_key":{"custom":true}`) {
		t.Fatalf("encoded JSON missing extension key: %s", string(encoded))
	}
}

func TestDecode_ToolChoiceErrors(t *testing.T) {
	t.Parallel()

	invalidToolChoices := []string{
		`{"model": "gpt-4o", "input": "hi", "tool_choice": "invalid_mode"}`,
		`{"model": "gpt-4o", "input": "hi", "tool_choice": {"type": "unknown_type"}}`,
		`{"model": "gpt-4o", "input": "hi", "tool_choice": {"type": "function"}}`, // missing function name
		`{"model": "gpt-4o", "input": "hi", "tool_choice": 12345}`,
	}

	for _, tcJSON := range invalidToolChoices {
		_, _, err := DecodeRequest([]byte(tcJSON))
		if err == nil {
			t.Fatalf("expected error for invalid tool_choice %s, got nil", tcJSON)
		}
	}
}

func TestBuildResponseResource_WithError(t *testing.T) {
	t.Parallel()

	env := EnvelopeMetadata{
		ResponseID: "resp_err",
		CreatedAt:  time.Now(),
		Model:      "gpt-4o",
		Status:     "failed",
	}

	streamErr := &lipapi.StreamError{
		Code:    "model_error",
		Message: "Model failed to process request",
	}

	wireRes, jsonBytes, err := BuildResponseResource(env, nil, UsageStats{}, lipapi.GenerationOptions{}, streamErr)
	if err != nil {
		t.Fatalf("BuildResponseResource failed with streamErr: %v", err)
	}

	if wireRes.Error == nil {
		t.Fatalf("expected non-nil Error in resource")
	}

	if !strings.Contains(string(jsonBytes), `"code":"model_error"`) {
		t.Fatalf("built JSON missing error code: %s", string(jsonBytes))
	}
}

func TestEncodeItem_UnknownKindError(t *testing.T) {
	t.Parallel()

	badItem := lipapi.Item{
		Kind: lipapi.ItemKind("unknown_kind"),
	}

	_, err := EncodeItem(badItem)
	if err == nil {
		t.Fatalf("expected error for unknown item kind, got nil")
	}
}

func TestDecode_AdditionalEdgeCases(t *testing.T) {
	t.Parallel()

	// Unexpected closing braces / brackets in strict JSON
	badJSONs := []string{
		`}`,
		`]`,
		`{"key": }`,
		`{"key": [1, 2}}`,
	}
	for _, bj := range badJSONs {
		_, _, err := DecodeRequest([]byte(bj))
		if err == nil {
			t.Fatalf("expected error for bad JSON %q, got nil", bj)
		}
	}

	// Unknown unprefixed top-level field
	_, _, err := DecodeRequest([]byte(`{"model": "gpt-4o", "input": "hi", "invalid_top_level": 123}`))
	if err == nil {
		t.Fatalf("expected error for unknown unprefixed top-level field, got nil")
	}

	// Tool missing name
	_, _, err = DecodeRequest([]byte(`{"model": "gpt-4o", "input": "hi", "tools": [{"type": "function"}]}`))
	if err == nil {
		t.Fatalf("expected error for tool missing name, got nil")
	}

	// ensureNonNilMap non-nil branch
	m := map[string]int{"a": 1}
	if res := ensureNonNilMap(m); res["a"] != 1 {
		t.Fatalf("ensureNonNilMap modified non-nil map")
	}
}

func TestStateMachine_EventErrorAndIncomplete(t *testing.T) {
	t.Parallel()

	envelope := EnvelopeMetadata{
		ResponseID: "resp_failed",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	// 1. EventError
	sm := NewStateMachine(envelope, lipapi.GenerationOptions{})
	_, _ = sm.ProcessCanonicalEvent(lipapi.Event{Kind: lipapi.EventResponseStarted})
	evs, err := sm.ProcessCanonicalEvent(lipapi.Event{
		Kind:         lipapi.EventError,
		ErrorCode:    "rate_limit_exceeded",
		ErrorMessage: "Rate limit exceeded",
	})
	if err != nil {
		t.Fatalf("EventError failed: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != "response.failed" {
		t.Fatalf("expected response.failed event, got %v", evs)
	}
	if sm.Status() != "failed" || sm.State() != StateTerminal {
		t.Fatalf("expected failed terminal state, got status=%s state=%s", sm.Status(), sm.State())
	}

	// 2. EventResponseFinished with max_tokens (incomplete)
	sm2 := NewStateMachine(envelope, lipapi.GenerationOptions{})
	_, _ = sm2.ProcessCanonicalEvent(lipapi.Event{Kind: lipapi.EventResponseStarted})
	evs, err = sm2.ProcessCanonicalEvent(lipapi.Event{
		Kind:         lipapi.EventResponseFinished,
		FinishReason: "length",
	})
	if err != nil {
		t.Fatalf("EventResponseFinished length failed: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != "response.incomplete" {
		t.Fatalf("expected response.incomplete event, got %v", evs)
	}
	if sm2.Status() != "incomplete" {
		t.Fatalf("expected status incomplete, got %s", sm2.Status())
	}
}

func TestStateMachine_DeepCloningAndSnapshotRestore(t *testing.T) {
	t.Parallel()

	envelope := EnvelopeMetadata{
		ResponseID: "resp_deep",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	sm := NewStateMachine(envelope, lipapi.GenerationOptions{})
	_, _ = sm.ProcessCanonicalEvent(lipapi.Event{Kind: lipapi.EventResponseStarted})
	_, _ = sm.ProcessCanonicalEvent(lipapi.Event{Kind: lipapi.EventMessageStarted})
	_, _ = sm.ProcessCanonicalEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "active text"})

	// Force an error while activeItem and activeContentPart are non-nil
	_, err := sm.ProcessCanonicalEvent(lipapi.Event{Kind: lipapi.EventKind("invalid_trigger_error")})
	if err == nil {
		t.Fatalf("expected error on invalid event")
	}

	// Verify state and active pointers are intact
	if sm.Trajectory()[0].Content[0].Text != "active text" {
		t.Fatalf("expected active text to be restored after error")
	}

	// Test cloneItems with all item kinds
	items := []lipapi.Item{
		{
			Kind: lipapi.ItemKindToolCall,
			ToolCall: &lipapi.ToolCallItem{
				CallID:    "c1",
				Name:      "fn",
				Arguments: []byte(`{"a":1}`),
			},
		},
		{
			Kind: lipapi.ItemKindToolResult,
			ToolResult: &lipapi.ToolResultItem{
				CallID: "c1",
				Name:   "fn",
				Parts:  []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "res"}},
			},
		},
		{
			Kind: lipapi.ItemKindReasoning,
			Reasoning: &lipapi.ReasoningItem{
				Reasoning: &lipapi.ReasoningPart{Text: "reasoning"},
			},
		},
		{
			Kind: lipapi.ItemKindCompaction,
			Compaction: &lipapi.CompactionItem{
				EncapsulatedID: "enc1",
				Opaque:         []byte(`{"o":1}`),
			},
		},
		{
			Kind: lipapi.ItemKindExtension,
			Extension: &lipapi.OpaqueExtension{
				Namespace: "ns",
				Type:      "ext:type",
				Data:      []byte(`{"d":1}`),
			},
		},
		{
			Kind: lipapi.ItemKindItemReference,
			Reference: &lipapi.ItemReference{
				ID: "ref1",
			},
		},
	}

	cloned := cloneItems(items)
	if len(cloned) != len(items) {
		t.Fatalf("expected %d cloned items, got %d", len(items), len(cloned))
	}
	if string(cloned[0].ToolCall.Arguments) != `{"a":1}` {
		t.Fatalf("cloned tool call args mismatch")
	}
	if cloned[1].ToolResult.Parts[0].Text != "res" {
		t.Fatalf("cloned tool result text mismatch")
	}
	if cloned[2].Reasoning.Reasoning.Text != "reasoning" {
		t.Fatalf("cloned reasoning text mismatch")
	}
	if string(cloned[3].Compaction.Opaque) != `{"o":1}` {
		t.Fatalf("cloned compaction opaque mismatch")
	}
	if string(cloned[4].Extension.Data) != `{"d":1}` {
		t.Fatalf("cloned extension data mismatch")
	}
	if cloned[5].Reference.ID != "ref1" {
		t.Fatalf("cloned reference ID mismatch")
	}
}

func TestDecode_ItemKindsAndContentParts(t *testing.T) {
	t.Parallel()

	// Item decode tests for all item wire types
	wireItemsJSON := []string{
		`{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Hello"}]}`,
		`{"type": "message", "role": "user", "content": [{"type": "input_image", "image_url": "https://img.jpg"}]}`,
		`{"type": "message", "role": "user", "content": [{"type": "acme:input_file", "file_url": "https://file.pdf"}]}`,
		`{"type": "message", "role": "assistant", "content": [{"type": "refusal", "refusal": "Cannot process"}]}`,
		`{"type": "function_call", "call_id": "c1", "name": "fn1", "arguments": "{\"x\":1}"}`,
		`{"type": "function_call_output", "call_id": "c1", "output": "output string"}`,
		`{"type": "reasoning", "summary": [{"type": "summary_text", "text": "sum"}], "content": [{"type": "output_text", "text": "reas"}]}`,
		`{"type": "item_reference", "id": "ref_1"}`,
		`{"type": "acme:custom_item", "data": {"a": 1}}`,
	}

	for _, wj := range wireItemsJSON {
		var wi WireItem
		if err := json.Unmarshal([]byte(wj), &wi); err != nil {
			t.Fatalf("unmarshal WireItem failed for %s: %v", wj, err)
		}
		item, err := DecodeItem(wi, DefaultLimits())
		if err != nil {
			t.Fatalf("DecodeItem failed for %s: %v", wj, err)
		}
		if string(item.Kind) == "" {
			t.Fatalf("expected non-empty item kind for %s", wj)
		}
	}
}
