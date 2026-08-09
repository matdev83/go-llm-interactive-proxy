package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNativeHistoryBuilder_PreservesExactReasoningActionTrajectories(t *testing.T) {
	call := nativeHistoryTrajectoryCall(nil)

	history, err := buildNativeHistory(call)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(history.Items), 6; got != want {
		t.Fatalf("history items = %d, want %d", got, want)
	}
	if got := nativeHistoryItemJSON(history.Items[1]); got != `{"id":"r1","type":"reasoning","summary":[],"encrypted_content":"opaque-r1","status":"completed"}` {
		t.Fatalf("reasoning envelope = %s", got)
	}
	if got := nativeHistoryItemJSON(history.Items[2]); !strings.Contains(got, `"call_id":"call-1"`) || !strings.Contains(got, `"name":"lookup"`) {
		t.Fatalf("function call identity/content = %s", got)
	}
	if got := nativeHistoryItemJSON(history.Items[3]); !strings.Contains(got, `"call_id":"call-1"`) {
		t.Fatalf("function output identity = %s", got)
	}
	wantTypes := []string{"message", "reasoning", "function_call", "function_call_output", "reasoning", "message"}
	for i, want := range wantTypes {
		body, err := json.Marshal(history.Items[i])
		if err != nil {
			t.Fatal(err)
		}
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(body, &header); err != nil {
			t.Fatal(err)
		}
		if header.Type != want {
			t.Fatalf("history item[%d] type = %q, want %q", i, header.Type, want)
		}
	}
	if got := history.Boundaries[1]; !got.AssistantStart || !got.PairSafe {
		t.Fatalf("assistant boundary = %+v", got)
	}
	if got := history.Boundaries[3]; got.PairSafe {
		t.Fatalf("call/output split must not be pair-safe: %+v", got)
	}
	if got := history.Boundaries[4]; !got.AssistantStart || !got.PairSafe {
		t.Fatalf("post-output assistant boundary = %+v", got)
	}
	if got, want := history.SafeSplitIndices(), []int{0, 1, 4, 6}; !equalInts(got, want) {
		t.Fatalf("safe split indices = %v, want %v", got, want)
	}
}

func TestNativeHistoryBuilder_IsIndependentOfNoToolsProjection(t *testing.T) {
	withoutTools := nativeHistoryTrajectoryCall(nil)
	withTools := nativeHistoryTrajectoryCall([]lipapi.ToolDef{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}})

	without, err := buildNativeHistory(withoutTools)
	if err != nil {
		t.Fatal(err)
	}
	with, err := buildNativeHistory(withTools)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := nativeHistoryJSON(without), nativeHistoryJSON(with); got != want {
		t.Fatalf("native history changed with normal tools projection:\n%s\n%s", got, want)
	}
	if got, err := buildInputItems(withoutTools); err != nil {
		t.Fatal(err)
	} else if len(got) == 0 || inputItemType(got[2]) != "message" {
		t.Fatalf("normal no-tools projection did not remain text-safe: %v", got)
	}
}

func TestNativeHistoryBuilder_PreservesCompactionAndDefensivelyCopiesOpaqueData(t *testing.T) {
	raw := json.RawMessage(`{"type":"compaction","id":"cmp-1","encrypted_content":"opaque"}`)
	call := &lipapi.Call{Items: []lipapi.Item{{
		Kind:       lipapi.ItemKindCompaction,
		Status:     lipapi.ItemStatusCompleted,
		Compaction: &lipapi.CompactionItem{Opaque: raw},
	}}}

	history, err := buildNativeHistory(call)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] = 'x'
	item, ok := history.Items[0].(opaqueResponseItem)
	if !ok {
		t.Fatalf("history item type = %T, want opaqueResponseItem", history.Items[0])
	}
	if string(item.raw) != `{"type":"compaction","id":"cmp-1","encrypted_content":"opaque"}` {
		t.Fatalf("opaque item was not copied: %s", item.raw)
	}
	if len(history.Fingerprints) != 1 || history.Fingerprints[0] == "" {
		t.Fatalf("fingerprints = %v", history.Fingerprints)
	}
}

func TestNativeHistoryBuilder_DefensivelyCopiesStructuredAndRichInput(t *testing.T) {
	argument := json.RawMessage(`{"query":"original"}`)
	call := &lipapi.Call{Items: []lipapi.Item{
		{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser, Content: []lipapi.ContentPart{
			{Kind: lipapi.ContentPartText, Text: "original text"},
			{Kind: lipapi.ContentPartImageRef, ImageRef: "image-original"},
		}},
		{Kind: lipapi.ItemKindToolCall, ToolCall: &lipapi.ToolCallItem{CallID: "call-1", Name: "lookup", Arguments: argument}},
		{Kind: lipapi.ItemKindToolResult, ToolResult: &lipapi.ToolResultItem{CallID: "call-1", Output: "original output"}},
	}}
	history, err := buildNativeHistory(call)
	if err != nil {
		t.Fatal(err)
	}
	call.Items[0].Content[0].Text = "mutated text"
	call.Items[0].Content[1].ImageRef = "image-mutated"
	argument[2] = 'X'
	call.Items[2].ToolResult.Output = "mutated output"

	message, ok := history.Items[0].(richMessageItem)
	if !ok {
		t.Fatalf("history item[0] type = %T, want richMessageItem", history.Items[0])
	}
	if got := message.Content[0].(inputTextPart).Text; got != "original text" {
		t.Fatalf("copied rich text = %q", got)
	}
	if got := message.Content[1].(inputImagePart).ImageURL; got != "image-original" {
		t.Fatalf("copied image ref = %q", got)
	}
	if got := history.Items[1].(functionCallItem).Arguments; got != `{"query":"original"}` {
		t.Fatalf("copied function arguments = %q", got)
	}
	if got := history.Items[2].(functionCallOutputItem).Output; got != "original output" {
		t.Fatalf("copied function output = %q", got)
	}
}

func TestNativeHistoryBuilder_FingerprintsAreStableAndTrackHistoryEdits(t *testing.T) {
	base := nativeHistoryTrajectoryCall(nil)
	first, err := buildNativeHistory(base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildNativeHistory(nativeHistoryTrajectoryCall(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(first.Fingerprints, second.Fingerprints) {
		t.Fatalf("same history fingerprints differ: %v != %v", first.Fingerprints, second.Fingerprints)
	}

	edited := nativeHistoryTrajectoryCall(nil)
	edited.Messages[0].Parts[0].Text = "edited"
	changed, err := buildNativeHistory(edited)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprints[0] == changed.Fingerprints[0] {
		t.Fatal("editing a message did not change its fingerprint")
	}

	reordered := nativeHistoryTrajectoryCall(nil)
	reordered.Messages[1], reordered.Messages[2] = reordered.Messages[2], reordered.Messages[1]
	if _, err := buildNativeHistory(reordered); err == nil {
		t.Fatal("reordering a call/output trajectory must be rejected")
	}
}

func TestNativeHistoryBuilder_RejectsUnsafeAndUnsupportedHistory(t *testing.T) {
	tests := []struct {
		name string
		call *lipapi.Call
		cat  string
	}{
		{
			name: "orphan output",
			call: &lipapi.Call{Items: []lipapi.Item{{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: "missing", Output: "secret-output"}}}},
			cat:  "orphan_output",
		},
		{
			name: "missing call id",
			call: &lipapi.Call{Items: []lipapi.Item{{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusCompleted, ToolCall: &lipapi.ToolCallItem{Name: "lookup", Arguments: json.RawMessage(`{}`)}}}},
			cat:  "missing_call_id",
		},
		{
			name: "duplicate call id",
			call: &lipapi.Call{Items: []lipapi.Item{
				{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusCompleted, ToolCall: &lipapi.ToolCallItem{CallID: "same", Name: "one", Arguments: json.RawMessage(`{}`)}},
				{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusCompleted, ToolCall: &lipapi.ToolCallItem{CallID: "same", Name: "two", Arguments: json.RawMessage(`{}`)}},
			}},
			cat: "duplicate_call_id",
		},
		{
			name: "incomplete item",
			call: &lipapi.Call{Items: []lipapi.Item{{Kind: lipapi.ItemKindMessage, Status: lipapi.ItemStatusIncomplete, Role: lipapi.RoleUser, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "unfinished"}}}}},
			cat:  "incomplete_item",
		},
		{
			name: "illegal role",
			call: &lipapi.Call{Items: []lipapi.Item{{Kind: lipapi.ItemKindMessage, Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleSystem, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "system"}}}}},
			cat:  "illegal_role",
		},
		{
			name: "illegal status",
			call: &lipapi.Call{Items: []lipapi.Item{{Kind: lipapi.ItemKindMessage, Status: "future", Role: lipapi.RoleUser, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "status"}}}}},
			cat:  "illegal_status",
		},
		{
			name: "unsupported reasoning dialect",
			call: &lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{Kind: lipapi.PartReasoning, Reasoning: &lipapi.ReasoningPart{Dialect: lipapi.ReasoningDialectOpenAIChatTextV1, Text: "do not expose"}}}}}},
			cat:  "unsupported_dialect",
		},
		{
			name: "malformed exact reasoning",
			call: &lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{Kind: lipapi.PartReasoning, Reasoning: &lipapi.ReasoningPart{Dialect: lipapi.ReasoningDialectOpenAIResponsesItemV1, Opaque: json.RawMessage(`{"type":"reasoning","encrypted_content":"private"`)}}}}}},
			cat:  "invalid_opaque",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := buildNativeHistoryExpectError(tt.call)
			if err == nil || !strings.Contains(err.Error(), tt.cat) {
				t.Fatalf("error = %v, want category %q", err, tt.cat)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "unfinished") {
				t.Fatalf("content leaked in error: %v", err)
			}
		})
	}
}

func TestNativeHistoryBuilder_RejectsOversizedOpaqueJSON(t *testing.T) {
	oversized := strings.Repeat("x", lipapi.MaxReasoningOpaqueBytes+1)
	call := &lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
		Kind:      lipapi.PartReasoning,
		Reasoning: &lipapi.ReasoningPart{Dialect: lipapi.ReasoningDialectOpenAIResponsesItemV1, Opaque: json.RawMessage(`{"id":"r","summary":[],"encrypted_content":"` + oversized + `"}`)},
	}}}}}
	err := buildNativeHistoryExpectError(call)
	if err == nil || !strings.Contains(err.Error(), "oversized") {
		t.Fatalf("error = %v, want oversized category", err)
	}
	if strings.Contains(err.Error(), oversized[:32]) {
		t.Fatalf("opaque payload leaked in error: %v", err)
	}
}

func TestNativeHistoryBuilder_FingerprintScopeCoversTruncationAndForks(t *testing.T) {
	base := nativeHistoryTrajectoryCall(nil)
	history, err := buildNativeHistory(base)
	if err != nil {
		t.Fatal(err)
	}

	truncated := nativeHistoryTrajectoryCall(nil)
	truncated.Messages = truncated.Messages[:1]
	truncatedHistory, err := buildNativeHistory(truncated)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Fingerprints) == len(truncatedHistory.Fingerprints) {
		t.Fatal("truncation did not change fingerprint scope")
	}

	fork := nativeHistoryTrajectoryCall(nil)
	fork.Messages[3].Parts[1].Text = "forked"
	forkHistory, err := buildNativeHistory(fork)
	if err != nil {
		t.Fatal(err)
	}
	if history.Fingerprints[5] == forkHistory.Fingerprints[5] {
		t.Fatal("forked assistant content did not change its fingerprint")
	}
}

func TestNativeHistoryBuilder_LegacyAssistantPartsKeepTextReasoningAndMultipleCalls(t *testing.T) {
	reasoning := func(id string) lipapi.Part {
		return lipapi.Part{Kind: lipapi.PartReasoning, Reasoning: &lipapi.ReasoningPart{
			Dialect: lipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"id":"` + id + `","type":"reasoning","summary":[],"encrypted_content":"` + id + `"}`),
		}}
	}
	call := &lipapi.Call{
		Messages: []lipapi.Message{
			{Role: lipapi.RoleDeveloper, Parts: []lipapi.Part{lipapi.TextPart("developer context")}},
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("system instructions")}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("do both")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{
				reasoning("r1"),
				lipapi.TextPart("before calls"),
				{Kind: lipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"call-1","name":"one","arguments":"{}"}`)},
				lipapi.TextPart("between calls"),
				{Kind: lipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"call-2","name":"two","arguments":"{\"x\":1}"}`)},
				lipapi.TextPart("after calls"),
			}},
			{Role: lipapi.RoleTool, Parts: []lipapi.Part{
				{Kind: lipapi.PartToolResult, ToolCallID: "call-2", Content: json.RawMessage(`{"result":"two"}`)},
				{Kind: lipapi.PartToolResult, ToolCallID: "call-1", Content: json.RawMessage(`{"result":"one"}`)},
			}},
		},
	}

	history, err := buildNativeHistory(call)
	if err != nil {
		t.Fatal(err)
	}
	wantTypes := []string{"message", "message", "reasoning", "message", "function_call", "message", "function_call", "message", "function_call_output", "function_call_output"}
	if len(history.Items) != len(wantTypes) {
		t.Fatalf("history items = %d, want %d", len(history.Items), len(wantTypes))
	}
	for i, want := range wantTypes {
		if got := inputItemType(history.Items[i]); got != want {
			t.Fatalf("history item[%d] type = %q, want %q", i, got, want)
		}
	}
	if got := history.Items[3].(textMessageItem).Content; got != "before calls" {
		t.Fatalf("text before calls = %q", got)
	}
	if got := history.Items[5].(textMessageItem).Content; got != "between calls" {
		t.Fatalf("text between calls = %q", got)
	}
	if got := history.Items[7].(textMessageItem).Content; got != "after calls" {
		t.Fatalf("text after calls = %q", got)
	}
	if got := history.Items[8].(functionCallOutputItem).CallID; got != "call-2" {
		t.Fatalf("first output call id = %q", got)
	}
	if got := history.Items[9].(functionCallOutputItem).CallID; got != "call-1" {
		t.Fatalf("second output call id = %q", got)
	}
}

func TestNativeTrajectoryBoundariesTrackOutstandingCallsAndAssistantTrajectories(t *testing.T) {
	items := []inputItem{
		textMessageItem{Type: "message", Role: "user", Content: "prompt"},
		opaqueResponseItem{raw: json.RawMessage(`{"type":"reasoning","id":"r1"}`)},
		functionCallItem{Type: "function_call", CallID: "call-1", Name: "one", Arguments: "{}"},
		functionCallItem{Type: "function_call", CallID: "call-2", Name: "two", Arguments: "{}"},
		functionCallOutputItem{Type: "function_call_output", CallID: "call-2", Output: "{}"},
		opaqueResponseItem{raw: json.RawMessage(`{"type":"reasoning","id":"r2"}`)},
		functionCallOutputItem{Type: "function_call_output", CallID: "call-1", Output: "{}"},
		textMessageItem{Type: "message", Role: "assistant", Content: "done"},
	}
	boundaries := nativeTrajectoryBoundaries(items)
	wantSafe := []bool{true, true, false, false, false, false, false, true, true}
	for i, want := range wantSafe {
		if boundaries[i].PairSafe != want {
			t.Fatalf("boundary[%d].PairSafe = %v, want %v; boundaries=%+v", i, boundaries[i].PairSafe, want, boundaries)
		}
	}
}

func TestNativeTrajectoryBoundariesKeepLiveCallOutOfCompactablePrefix(t *testing.T) {
	items := []inputItem{
		textMessageItem{Type: "message", Role: "user", Content: "prompt"},
		functionCallItem{Type: "function_call", CallID: "live", Name: "lookup", Arguments: "{}"},
	}
	boundaries := nativeTrajectoryBoundaries(items)
	if boundaries[len(boundaries)-1].PairSafe {
		t.Fatalf("final boundary after unmatched call must be unsafe: %+v", boundaries)
	}
}

func TestNativeHistoryBuilder_EnforcesAggregateHistoryBytes(t *testing.T) {
	const itemText = 8192
	messages := make([]lipapi.Message, 0, maxNativeHistoryItems)
	for i := 0; i < maxNativeHistoryItems; i++ {
		messages = append(messages, lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: strings.Repeat("x", itemText)}}})
	}
	_, err := buildNativeHistory(&lipapi.Call{Messages: messages})
	if err == nil || !strings.Contains(err.Error(), "history_bounds") {
		t.Fatalf("error = %v, want aggregate history_bounds", err)
	}
}

func TestNativeHistoryBuilder_FingerprintUsesSemanticOpaqueJSON(t *testing.T) {
	makeCall := func(raw string) *lipapi.Call {
		return &lipapi.Call{Items: []lipapi.Item{{
			Kind: lipapi.ItemKindCompaction, Status: lipapi.ItemStatusCompleted,
			Compaction: &lipapi.CompactionItem{Opaque: json.RawMessage(raw)},
		}}}
	}
	first, err := buildNativeHistory(makeCall(`{"type":"compaction","id":"c1","encrypted_content":"opaque"}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildNativeHistory(makeCall(`{ "encrypted_content" : "opaque", "id" : "c1", "type" : "compaction" }`))
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(first.Fingerprints, second.Fingerprints) {
		t.Fatalf("semantic opaque JSON fingerprints differ: %v != %v", first.Fingerprints, second.Fingerprints)
	}
}

func nativeHistoryTrajectoryCall(tools []lipapi.ToolDef) *lipapi.Call {
	reasoning := func(id string) lipapi.Part {
		return lipapi.Part{Kind: lipapi.PartReasoning, Reasoning: &lipapi.ReasoningPart{
			Dialect: lipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"id":"` + id + `","type":"reasoning","summary":[],"encrypted_content":"opaque-` + id + `","status":"completed"}`),
		}}
	}
	return &lipapi.Call{
		Tools: tools,
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("use lookup")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{
				reasoning("r1"),
				{Kind: lipapi.PartJSON, ToolCallID: "call-1", ToolName: "lookup", Content: json.RawMessage(`{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{\"q\":\"x\"}"}`)},
			}},
			{Role: lipapi.RoleTool, Parts: []lipapi.Part{{Kind: lipapi.PartToolResult, ToolCallID: "call-1", Content: json.RawMessage(`{"answer":"y"}`)}}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{reasoning("r2"), lipapi.TextPart("done")}},
		},
	}
}

func buildNativeHistoryExpectError(call *lipapi.Call) error {
	_, err := buildNativeHistory(call)
	return err
}

func nativeHistoryJSON(history NativeHistory) string {
	items := make([]json.RawMessage, 0, len(history.Items))
	for _, item := range history.Items {
		body, err := json.Marshal(item)
		if err != nil {
			return "marshal-error"
		}
		items = append(items, body)
	}
	body, _ := json.Marshal(items)
	return string(body)
}

func nativeHistoryItemJSON(item inputItem) string {
	body, _ := json.Marshal(item)
	return string(body)
}

func inputItemType(item inputItem) string {
	body, _ := json.Marshal(item)
	var header struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(body, &header)
	return header.Type
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
