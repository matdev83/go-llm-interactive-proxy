package secretguard

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestWalkLogicalFragments_PreservesCanonicalContextAndLocations(t *testing.T) {
	t.Parallel()

	toolSchema := json.RawMessage(`{"type":"object","properties":{"api_key":{"type":"string"}}}`)
	toolResult := json.RawMessage(`{"api_key":"opaque-result"}`)
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Parts: []lipapi.Part{
				lipapi.TextPart("instruction text"),
				{Kind: lipapi.PartJSON, Content: json.RawMessage(`{"api_key":"opaque-argument"}`)},
				{Kind: lipapi.PartToolResult, Text: "tool result text", Content: toolResult},
			},
		}},
		Tools: []lipapi.ToolDef{{
			Name:        "lookup",
			Description: "lookup description",
			Parameters:  toolSchema,
		}},
	}

	want := []struct {
		location string
		kind     FragmentKind
		raw      string
	}{
		{location: "instructions[0].parts[0]", kind: FragmentText, raw: "instruction text"},
		{location: "instructions[0].parts[1]", kind: FragmentJSON, raw: `{"api_key":"opaque-argument"}`},
		{location: "instructions[0].parts[2]", kind: FragmentText, raw: "tool result text"},
		{location: "instructions[0].parts[2]", kind: FragmentJSON, raw: `{"api_key":"opaque-result"}`},
		{location: "tools[0].name", kind: FragmentText, raw: "lookup"},
		{location: "tools[0].description", kind: FragmentText, raw: "lookup description"},
		{location: "tools[0].schema", kind: FragmentJSON, raw: `{"type":"object","properties":{"api_key":{"type":"string"}}}`},
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
		if got := string(fragment.Raw); got != want[i].raw {
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

func TestWalkLogicalFragments_AdmitsWholeFragmentsAgainstSharedBudget(t *testing.T) {
	t.Parallel()

	first := "first logical text"
	second := `{"api_key":"second"}`
	call := &lipapi.Call{
		Messages: []lipapi.Message{{
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
	if got := string(fragments[0].Raw); got != first {
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
		Messages: []lipapi.Message{{Parts: []lipapi.Part{
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
	call := &lipapi.Call{Messages: []lipapi.Message{{Parts: []lipapi.Part{{
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
