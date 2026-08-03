package openresponses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// TestContentPartExtension_NamespaceDerivedOnDecode proves that decoding a
// vendor-prefixed wire content part materializes the deterministic namespace
// and that re-encoding does not inject a redundant namespace field (the wire
// stays lossless).
func TestContentPartExtension_NamespaceDerivedOnDecode(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"input": []any{contentMessage("user",
			map[string]any{"type": "acme:input_file", "file_url": "https://x/f"},
		)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, call, err := DecodeRequest(body)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	cp := call.Items[0].Content[0]
	if cp.Kind != lipapi.ContentPartExtension || cp.Extension == nil {
		t.Fatalf("expected extension content part, got %+v", cp)
	}
	if cp.Extension.Namespace != "acme" {
		t.Fatalf("derived namespace = %q, want acme", cp.Extension.Namespace)
	}
	encoded, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if strings.Contains(string(encoded), `"namespace"`) {
		t.Fatalf("re-encoded wire injected a derived namespace field: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"file_url":"https://x/f"`) {
		t.Fatalf("re-encoded wire lost file_url: %s", encoded)
	}
}

// TestContentPartExtension_WireNamespaceAndImplementorRoundTrip proves that an
// explicit wire namespace/implementor is carried exactly and preserved on
// re-encoding.
func TestContentPartExtension_WireNamespaceAndImplementorRoundTrip(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"input": []any{contentMessage("user",
			map[string]any{"type": "acme:widget", "namespace": "custom", "implementor": "acme-vendor", "k": 1},
		)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, call, err := DecodeRequest(body)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	cp := call.Items[0].Content[0]
	if cp.Extension == nil || cp.Extension.Namespace != "custom" || cp.Extension.Implementor != "acme-vendor" {
		t.Fatalf("canonical extension metadata mismatch: %+v", cp.Extension)
	}
	encoded, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var out struct {
		Input []struct {
			Content []map[string]json.RawMessage `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("re-encoded request: %v", err)
	}
	part := out.Input[0].Content[0]
	if got := string(part["namespace"]); got != `"custom"` {
		t.Fatalf("re-encoded namespace = %s, want \"custom\"", got)
	}
	if got := string(part["implementor"]); got != `"acme-vendor"` {
		t.Fatalf("re-encoded implementor = %s, want \"acme-vendor\"", got)
	}
}

// TestContentPartExtension_ExplicitNamespaceMergedOnEncode proves that a
// canonical part carrying an explicit namespace diverging from the derived one
// is merged into the emitted wire object instead of silently dropped.
func TestContentPartExtension_ExplicitNamespaceMergedOnEncode(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{Items: []lipapi.Item{{
		Kind: lipapi.ItemKindMessage,
		Role: lipapi.RoleUser,
		Content: []lipapi.ContentPart{{
			Kind: lipapi.ContentPartExtension,
			Extension: &lipapi.ExtensionContentPart{
				Namespace:   "custom",
				Type:        "acme:part",
				Implementor: "acme-vendor",
				Data:        json.RawMessage(`{"type":"acme:part","payload":{"k":1}}`),
			},
		}},
	}}}
	encoded, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var out struct {
		Input []struct {
			Content []map[string]json.RawMessage `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("re-encoded request: %v", err)
	}
	part := out.Input[0].Content[0]
	if got := string(part["namespace"]); got != `"custom"` {
		t.Fatalf("merged namespace = %s, want \"custom\"", got)
	}
	if got := string(part["implementor"]); got != `"acme-vendor"` {
		t.Fatalf("merged implementor = %s, want \"acme-vendor\"", got)
	}
	if got := string(part["payload"]); got != `{"k":1}` {
		t.Fatalf("payload = %s, want preserved", got)
	}
}

// TestContentPartExtension_ToolResultStructuredPartRoundTrip proves extension
// content parts inside structured tool-result output survive decode and encode.
func TestContentPartExtension_ToolResultStructuredPartRoundTrip(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"input": []any{contentMessage("user", map[string]any{"type": "input_text", "text": "hi"})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, call, err := DecodeRequest(body)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	call.Items = append(call.Items, lipapi.Item{
		Kind: lipapi.ItemKindToolResult,
		ID:   "tr-1",
		ToolResult: &lipapi.ToolResultItem{
			CallID: "call-1",
			Name:   "lookup",
			Parts: []lipapi.ContentPart{{
				Kind: lipapi.ContentPartExtension,
				Extension: &lipapi.ExtensionContentPart{
					Type: "acme:result",
					Data: json.RawMessage(`{"type":"acme:result","rows":2}`),
				},
			}},
		},
	})
	encoded, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var out struct {
		Input []struct {
			Type   string                       `json:"type"`
			Output []map[string]json.RawMessage `json:"output"`
		} `json:"input"`
	}
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("re-encoded request: %v", err)
	}
	var found *map[string]json.RawMessage
	for i := range out.Input {
		if out.Input[i].Type == "function_call_output" {
			for j := range out.Input[i].Output {
				if string(out.Input[i].Output[j]["type"]) == `"acme:result"` {
					found = &out.Input[i].Output[j]
				}
			}
		}
	}
	if found == nil {
		t.Fatalf("re-encoded tool result lost the structured extension part: %s", encoded)
	}
	if got := string((*found)["rows"]); got != "2" {
		t.Fatalf("tool-result extension payload rows = %s, want 2", got)
	}
}
