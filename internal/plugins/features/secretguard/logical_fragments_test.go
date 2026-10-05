package secretguard

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func requireValidCanonicalCall(t *testing.T, call *lipapi.Call) {
	t.Helper()
	if err := call.Validate(); err != nil {
		t.Fatalf("canonical fixture is invalid: %v", err)
	}
}

func TestWalkLogicalFragments_PreservesCanonicalContextAndLocations(t *testing.T) {
	t.Parallel()

	toolSchema := json.RawMessage(`{"type":"object","properties":{"api_key":{"type":"string"}}}`)
	toolResult := json.RawMessage(`{"api_key":"opaque-result"}`)
	call := &lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.TextPart("user prompt text"),
				{Kind: lipapi.PartJSON, Content: json.RawMessage(`{"api_key":"opaque-prompt"}`)},
			},
		}, {
			Role: lipapi.RoleTool,
			Parts: []lipapi.Part{
				{Kind: lipapi.PartToolResult, ToolCallID: "call-1", Text: "tool result text", Content: toolResult},
			},
		}},
		Tools: []lipapi.ToolDef{{
			Name:        "lookup",
			Description: "lookup description",
			Parameters:  toolSchema,
		}},
	}
	requireValidCanonicalCall(t, call)

	want := []struct {
		location string
		kind     FragmentKind
		raw      string
	}{
		{location: "messages[0].parts[0]", kind: FragmentText, raw: "user prompt text"},
		{location: "messages[0].parts[1]", kind: FragmentJSON, raw: `{"api_key":"opaque-prompt"}`},
		{location: "messages[1].parts[0]", kind: FragmentText, raw: "tool result text"},
		{location: "messages[1].parts[0]", kind: FragmentJSON, raw: `{"api_key":"opaque-result"}`},
	}

	budget := newScanBudget(1024)
	fragments := walkLogicalFragments(call, budget)
	if len(fragments) != len(want) {
		t.Fatalf("fragment count: got %d want %d: %#v", len(fragments), len(want), fragments)
	}
	for i, fragment := range fragments {
		if got := fragment.Location; got != want[i].location {
			t.Errorf("fragment %d location: got %q want %q", i, got, want[i].location)
		}
		if got := fragment.Kind; got != want[i].kind {
			t.Errorf("fragment %d kind: got %v want %v", i, got, want[i].kind)
		}
		if got := fragment.textValue(); got != want[i].raw {
			t.Errorf("fragment %d raw: got %q want %q", i, got, want[i].raw)
		}
	}

	// Equal location/text values are still separate logical occurrences because
	// the text and raw JSON fields belong to different request content fields.
	if got := fragments[2].Location; got != fragments[3].Location {
		t.Fatalf("tool-result fields must retain their canonical location: %q vs %q", got, fragments[3].Location)
	}
	if reflect.DeepEqual(fragments[2].Raw, fragments[3].Raw) {
		t.Fatal("text and JSON tool-result fields must remain distinct fragments")
	}
}

func TestWalkLogicalFragments_MessageAuthorityRestrictsProvenanceAndBudget(t *testing.T) {
	t.Parallel()

	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "instruction secret"}},
		}},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "system secret"}}},
			{Role: lipapi.RoleDeveloper, Parts: []lipapi.Part{{Kind: lipapi.PartJSON, Content: json.RawMessage(`{"token":"developer secret"}`)}}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "assistant secret"}}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{
				lipapi.TextPart("user prompt"),
				{Kind: lipapi.PartJSON, Content: json.RawMessage(`{"api_key":"user json"}`)},
			}},
			{Role: lipapi.RoleTool, Parts: []lipapi.Part{
				lipapi.TextPart("tool output"),
				{Kind: lipapi.PartToolResult, ToolCallID: "call-1", Text: "tool result text", Content: json.RawMessage(`{"token":"tool result json"}`)},
			}},
		},
		Tools: []lipapi.ToolDef{{
			Name:        "tool secret",
			Description: "tool description secret",
			Parameters:  json.RawMessage(`{"api_key":"tool schema secret"}`),
		}},
	}
	requireValidCanonicalCall(t, call)

	budget := newScanBudget(len("user prompt") + len(`{"api_key":"user json"}`) + len("tool output") + len("tool result text") + len(`{"token":"tool result json"}`))
	fragments := walkLogicalFragments(call, budget)
	want := []string{
		"user prompt",
		`{"api_key":"user json"}`,
		"tool output",
		"tool result text",
		`{"token":"tool result json"}`,
	}
	if len(fragments) != len(want) {
		t.Fatalf("fragment count: got %d want %d: %#v", len(fragments), len(want), fragments)
	}
	for i, fragment := range fragments {
		if got := fragment.textValue(); got != want[i] {
			t.Errorf("fragment %d: got %q want %q", i, got, want[i])
		}
	}
	if budget.limitHit {
		t.Fatal("eligible fragments should fit the shared budget")
	}
	if got, wantBytes := budget.used, budget.maxBytes; got != wantBytes {
		t.Fatalf("budget used: got %d want %d", got, wantBytes)
	}
}

func TestWalkLogicalFragments_ItemAuthoritySkipsUnknownRoleRobustness(t *testing.T) {
	t.Parallel()

	// Unknown item roles are rejected by the canonical item schema. Keep this
	// direct walker robustness fixture separate from validated SDK cases.
	call := &lipapi.Call{Items: []lipapi.Item{
		{Kind: lipapi.ItemKindMessage, Role: lipapi.Role("unknown"), Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "unknown secret"}}},
		{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "user prompt"}}},
	}}
	fragments := walkLogicalFragments(call, newScanBudget(1024))
	if len(fragments) != 1 || fragments[0].Location != "items[1].content[0]" || fragments[0].textValue() != "user prompt" {
		t.Fatalf("unknown role traversal: got %#v", fragments)
	}
}

func TestWalkLogicalFragments_ItemAuthorityRestrictsProvenanceAndBudget(t *testing.T) {
	t.Parallel()

	call := &lipapi.Call{Items: []lipapi.Item{
		{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleAssistant, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "assistant history"}}},
		{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser, Content: []lipapi.ContentPart{
			{Kind: lipapi.ContentPartText, Text: "item user prompt"},
			{Kind: lipapi.ContentPartJSON, Text: `{"api_key":"item prompt json"}`},
		}},
		{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleTool, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "item tool message"}}},
		{Kind: lipapi.ItemKindToolCall, ToolCall: &lipapi.ToolCallItem{CallID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"token":"model argument"}`)}},
		{Kind: lipapi.ItemKindToolResult, ToolResult: &lipapi.ToolResultItem{
			CallID: "call-1", Name: "lookup", Output: `{"token":"item tool output"}`,
		}},
		{Kind: lipapi.ItemKindToolCall, ToolCall: &lipapi.ToolCallItem{CallID: "call-2", Name: "lookup", Arguments: json.RawMessage(`{"token":"model argument 2"}`)}},
		{Kind: lipapi.ItemKindToolResult, ToolResult: &lipapi.ToolResultItem{
			CallID: "call-2", Name: "lookup",
			Parts: []lipapi.ContentPart{
				{Kind: lipapi.ContentPartText, Text: "item tool result text"},
				{Kind: lipapi.ContentPartJSON, Text: `{"api_key":"item result json"}`},
			},
		}},
		{Kind: lipapi.ItemKindItemReference, Reference: &lipapi.ItemReference{ID: "prior"}},
	}}
	requireValidCanonicalCall(t, call)

	want := []string{
		"item user prompt",
		`{"api_key":"item prompt json"}`,
		"item tool message",
		`{"token":"item tool output"}`,
		"item tool result text",
		`{"api_key":"item result json"}`,
	}
	budget := newScanBudget(len("item user prompt") + len(`{"api_key":"item prompt json"}`) + len("item tool message") + len(`{"token":"item tool output"}`) + len("item tool result text") + len(`{"api_key":"item result json"}`))
	fragments := walkLogicalFragments(call, budget)
	if len(fragments) != len(want) {
		t.Fatalf("fragment count: got %d want %d: %#v", len(fragments), len(want), fragments)
	}
	for i, fragment := range fragments {
		if got := fragment.textValue(); got != want[i] {
			t.Errorf("fragment %d: got %q want %q", i, got, want[i])
		}
	}
	if budget.limitHit {
		t.Fatal("eligible item fragments should fit the shared budget")
	}
	if got, wantBytes := budget.used, budget.maxBytes; got != wantBytes {
		t.Fatalf("budget used: got %d want %d", got, wantBytes)
	}
}

func TestWalkLogicalFragments_ExcludedOnlyContentDoesNotConsumeBudget(t *testing.T) {
	t.Parallel()

	call := &lipapi.Call{
		Instructions: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "instruction only"}}}},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "system only"}}},
			{Role: lipapi.RoleDeveloper, Parts: []lipapi.Part{{Kind: lipapi.PartJSON, Content: json.RawMessage(`{"token":"developer only"}`)}}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "assistant only"}}},
		},
		Tools: []lipapi.ToolDef{{Name: "tool only", Description: "description only", Parameters: json.RawMessage(`{"token":"schema only"}`)}},
	}
	requireValidCanonicalCall(t, call)

	budget := newScanBudget(1)
	if fragments := walkLogicalFragments(call, budget); len(fragments) != 0 {
		t.Fatalf("excluded content produced fragments: %#v", fragments)
	}
	if budget.used != 0 {
		t.Fatalf("excluded content consumed budget: got %d", budget.used)
	}
	if budget.limitHit {
		t.Fatal("excluded content must not trigger the scan limit")
	}
}

func TestWalkLogicalFragments_TextUsesImmutableStringRepresentation(t *testing.T) {
	const content = "immutable text fragment"
	call := &lipapi.Call{Messages: []lipapi.Message{{
		Role:  lipapi.RoleUser,
		Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: content}},
	}}}

	fragments := walkLogicalFragments(call, newScanBudget(len(content)))
	if len(fragments) != 1 {
		t.Fatalf("fragments=%d, want 1", len(fragments))
	}
	fragment := fragments[0]
	if fragment.Kind != FragmentText {
		t.Fatalf("kind=%v, want text", fragment.Kind)
	}
	if fragment.Text != content {
		t.Fatalf("text=%q, want %q", fragment.Text, content)
	}
	if fragment.Raw != nil {
		t.Fatalf("text fragment retained a byte representation of length %d", len(fragment.Raw))
	}
}

func TestWalkLogicalFragments_ItemAuthorityCoversMessagesAndToolResults(t *testing.T) {
	t.Parallel()

	call := &lipapi.Call{Items: []lipapi.Item{
		{
			Kind: lipapi.ItemKindMessage,
			Role: lipapi.RoleUser,
			Content: []lipapi.ContentPart{
				{Kind: lipapi.ContentPartText, Text: "item message text"},
				{Kind: lipapi.ContentPartJSON, Text: `{"credentials":{"token":"item-json"}}`},
			},
		},
		{
			Kind:     lipapi.ItemKindToolCall,
			ToolCall: &lipapi.ToolCallItem{CallID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"token":"item-arguments"}`)},
		},
		{
			Kind: lipapi.ItemKindToolResult,
			ToolResult: &lipapi.ToolResultItem{
				CallID: "call-1", Name: "lookup",
				Output: `{"token":"item-output"}`,
			},
		},
		{
			Kind:     lipapi.ItemKindToolCall,
			ToolCall: &lipapi.ToolCallItem{CallID: "call-2", Name: "lookup", Arguments: json.RawMessage(`{"token":"item-arguments-2"}`)},
		},
		{
			Kind: lipapi.ItemKindToolResult,
			ToolResult: &lipapi.ToolResultItem{
				CallID: "call-2", Name: "lookup",
				Parts: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "item result text"},
					{Kind: lipapi.ContentPartJSON, Text: `{"token":"item-result-json"}`},
				},
			},
		},
	}}
	requireValidCanonicalCall(t, call)
	want := []struct {
		location string
		kind     FragmentKind
		raw      string
	}{
		{location: "items[0].content[0]", kind: FragmentText, raw: "item message text"},
		{location: "items[0].content[1]", kind: FragmentJSON, raw: `{"credentials":{"token":"item-json"}}`},
		{location: "items[2].tool_result.output", kind: FragmentText, raw: `{"token":"item-output"}`},
		{location: "items[4].tool_result.parts[0]", kind: FragmentText, raw: "item result text"},
		{location: "items[4].tool_result.parts[1]", kind: FragmentJSON, raw: `{"token":"item-result-json"}`},
	}

	fragments := walkLogicalFragments(call, newScanBudget(1024))
	if len(fragments) != len(want) {
		t.Fatalf("item fragment count: got %d want %d", len(fragments), len(want))
	}
	for i, fragment := range fragments {
		if fragment.Location != want[i].location || fragment.Kind != want[i].kind || fragment.textValue() != want[i].raw {
			t.Fatalf("item fragment %d mismatch: got location=%q kind=%v raw_len=%d", i, fragment.Location, fragment.Kind, len(fragment.rawBytes()))
		}
	}

	fragments[0].setRaw([]byte("item message replacement"))
	fragments[1].setRaw([]byte(`{"credentials":{"token":"item-json-replacement"}}`))
	if got := call.Items[0].Content[0].Text; got != "item message replacement" {
		t.Fatalf("item message replacement was not committed: got %q", got)
	}
	if got := call.Items[0].Content[1].Text; got != `{"credentials":{"token":"item-json-replacement"}}` {
		t.Fatalf("item JSON replacement was not committed: got %q", got)
	}
}

func TestWalkLogicalFragments_AdmitsWholeFragmentsAgainstSharedBudget(t *testing.T) {
	t.Parallel()

	first := "first logical text"
	second := `{"api_key":"second"}`
	call := &lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.TextPart(first),
				{Kind: lipapi.PartJSON, Content: json.RawMessage(second)},
			},
		}},
	}

	budget := newScanBudget(len(first) + len(second) - 1)
	fragments := walkLogicalFragments(call, budget)
	if len(fragments) != 1 {
		t.Fatalf("admitted fragments: got %d want 1: %#v", len(fragments), fragments)
	}
	if got := fragments[0].textValue(); got != first {
		t.Fatalf("first fragment: got %q want %q", got, first)
	}
	if !budget.limitHit {
		t.Fatal("expected whole-fragment admission to set the scan limit")
	}
	if got, want := budget.used, len(first); got != want {
		t.Fatalf("budget used: got %d want %d", got, want)
	}

	// Detector passes consume the same admitted set. Reusing the fragments does
	// not reserve their original bytes a second time for derived JSON values.
	for range fragments {
		if got, want := budget.used, len(first); got != want {
			t.Fatalf("shared budget changed while reusing admitted fragment: got %d want %d", got, want)
		}
	}
}

func TestWalkLogicalFragments_IdenticalBytesInDistinctFieldsCountSeparately(t *testing.T) {
	t.Parallel()

	value := "same logical bytes"
	call := &lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{
			lipapi.TextPart(value),
			lipapi.TextPart(value),
		}}},
	}

	budget := newScanBudget(len(value) * 2)
	fragments := walkLogicalFragments(call, budget)
	if len(fragments) != 2 {
		t.Fatalf("fragment count: got %d want 2", len(fragments))
	}
	if got, want := budget.used, len(value)*2; got != want {
		t.Fatalf("identical fields must be charged separately: got %d want %d", got, want)
	}
}

func TestScanCall_UsesOneAdmissionForJSONScalarInspection(t *testing.T) {
	t.Parallel()

	raw := `{"api_key":"opaque"}`
	call := &lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{
		Kind:    lipapi.PartJSON,
		Content: json.RawMessage(raw),
	}}}}}
	m := newRecordingJSONMatcher()
	out, err := scanCall(t.Context(), call, m, modeScan, len(raw))
	if err != nil {
		t.Fatal(err)
	}
	if out.ScanLimitHit {
		t.Fatal("exact scalar inspection must stay within the raw-fragment budget")
	}
	if got, want := out.BytesScanned, len(raw); got != want {
		t.Fatalf("bytes scanned: got %d want %d", got, want)
	}
	if len(m.scanTokens) == 0 {
		t.Fatal("expected exact JSON inspection to retain key/value context")
	}

	limited := newRecordingJSONMatcher()
	out, err = scanCall(t.Context(), call, limited, modeScan, len(raw)-1)
	if err != nil {
		t.Fatal(err)
	}
	if !out.ScanLimitHit {
		t.Fatal("whole raw JSON fragment should trigger the existing scan limit")
	}
	if len(limited.scanTokens) != 0 {
		t.Fatalf("over-budget fragment must not be inspected: %#v", limited.scanTokens)
	}
}
