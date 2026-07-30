package responseitem

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestCanonizeReasoningItemOpaque_PreservesEncryptedContentPresence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		raw         string
		wantPresent bool
		wantRaw     string
	}{
		{name: "absent", raw: `{"id":"rs_1","summary":[]}`},
		{name: "null", raw: `{"id":"rs_1","summary":[],"encrypted_content":null}`, wantPresent: true, wantRaw: "null"},
		{name: "value", raw: `{"id":"rs_1","summary":[],"encrypted_content":"state"}`, wantPresent: true, wantRaw: `"state"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := CanonizeReasoningItemOpaque([]byte(tt.raw))
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]json.RawMessage
			if err := json.Unmarshal(got, &value); err != nil {
				t.Fatal(err)
			}
			raw, present := value["encrypted_content"]
			if present != tt.wantPresent {
				t.Fatalf("encrypted_content present = %v, want %v", present, tt.wantPresent)
			}
			if present && string(raw) != tt.wantRaw {
				t.Fatalf("encrypted_content = %s, want %s", raw, tt.wantRaw)
			}
		})
	}
}

func TestCanonizeReasoningItemOpaque_ValidationAndOrdering(t *testing.T) {
	t.Parallel()

	valid := `{"status":"completed","encrypted_content":null,"summary":[],"id":"rs_1"}`
	got, err := CanonizeReasoningItemOpaque([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":null,"status":"completed"}`
	if string(got) != want {
		t.Fatalf("canonical JSON = %s, want %s", got, want)
	}

	oversize := `{"id":"rs_1","summary":[],"encrypted_content":"` + strings.Repeat("x", lipapi.MaxReasoningOpaqueBytes) + `"}`
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "unknown field", raw: `{"id":"rs_1","summary":[],"extra":true}`},
		{name: "duplicate key", raw: `{"id":"rs_1","id":"rs_2","summary":[]}`},
		{name: "trailing JSON", raw: `{"id":"rs_1","summary":[]} true`},
		{name: "null content", raw: `{"id":"rs_1","summary":[],"content":null}`},
		{name: "invalid type", raw: `{"id":"rs_1","type":"message","summary":[]}`},
		{name: "invalid status", raw: `{"id":"rs_1","summary":[],"status":"done"}`},
		{name: "oversize", raw: oversize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := CanonizeReasoningItemOpaque([]byte(tc.raw)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestMarshalEnvelopeRejectsInvalidFields(t *testing.T) {
	t.Parallel()

	for _, fields := range []map[string]json.RawMessage{
		{"unknown": json.RawMessage(`true`)},
		{"id": json.RawMessage(`not-json`)},
	} {
		if _, err := MarshalEnvelope(fields); err == nil {
			t.Fatal("expected marshal error")
		}
	}
}
