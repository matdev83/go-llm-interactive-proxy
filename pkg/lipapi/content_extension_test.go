package lipapi_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func extContentPart(typ, data string) lipapi.ContentPart {
	return lipapi.ContentPart{
		Kind: lipapi.ContentPartExtension,
		Extension: &lipapi.ExtensionContentPart{
			Type: typ,
			Data: json.RawMessage(data),
		},
	}
}

func itemWith(parts ...lipapi.ContentPart) lipapi.Item {
	return lipapi.Item{
		Kind:    lipapi.ItemKindMessage,
		ID:      "item-1",
		Status:  lipapi.ItemStatusCompleted,
		Role:    lipapi.RoleUser,
		Content: parts,
	}
}

func callWith(items ...lipapi.Item) lipapi.Call {
	return lipapi.Call{ID: "call-1", Items: items}
}

func TestContentPartExtension_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		part    lipapi.ContentPart
		wantErr string
	}{
		{
			name: "valid prefixed colon extension",
			part: extContentPart("acme:input_file", `{"type":"acme:input_file","file_url":"https://x/f"}`),
		},
		{
			name: "valid prefixed slash extension",
			part: extContentPart("acme.com/part", `{"type":"acme.com/part","payload":{"k":1}}`),
		},
		{
			name:    "missing type",
			part:    extContentPart("", `{"type":"acme:input_file"}`),
			wantErr: "extension type is required",
		},
		{
			name:    "unprefixed type rejected",
			part:    extContentPart("invented_type", `{"type":"invented_type"}`),
			wantErr: "vendor-prefixed",
		},
		{
			name:    "missing extension record",
			part:    lipapi.ContentPart{Kind: lipapi.ContentPartExtension},
			wantErr: "requires Extension",
		},
		{
			name:    "missing data",
			part:    extContentPart("acme:part", ""),
			wantErr: "requires Data",
		},
		{
			name:    "invalid json data",
			part:    extContentPart("acme:part", `{not json`),
			wantErr: "must be valid JSON",
		},
		{
			name:    "data type mismatch",
			part:    extContentPart("acme:part", `{"type":"other:part"}`),
			wantErr: "must match Type",
		},
		{
			name:    "non-object data rejected",
			part:    extContentPart("acme:part", `[1,2]`),
			wantErr: "must be a JSON object",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			call := callWith(itemWith(tt.part))
			err := call.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected validation error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestContentPartExtension_BoundedData(t *testing.T) {
	t.Parallel()
	// Size bound is enforced before JSON parsing, so an oversized raw payload is
	// rejected without allocation-heavy JSON validation.
	part := extContentPart("acme:part", strings.Repeat(" ", lipapi.MaxExtensionDataBytes+1))
	if err := callWith(itemWith(part)).Validate(); err == nil {
		t.Fatal("expected oversized extension data rejection")
	}
}

func TestContentPartFileRef_RequiresRefOrData(t *testing.T) {
	t.Parallel()
	if err := callWith(itemWith(lipapi.ContentPart{Kind: lipapi.ContentPartFileRef})).Validate(); err == nil {
		t.Fatal("expected error for empty FileRef and FileData")
	}
	if err := callWith(itemWith(lipapi.ContentPart{Kind: lipapi.ContentPartFileRef, FileRef: "https://x/f.pdf", FileData: "aGVsbG8=", FileName: "f.pdf"})).Validate(); err != nil {
		t.Fatalf("unexpected error for ref+data file part: %v", err)
	}
	if err := callWith(itemWith(lipapi.ContentPart{Kind: lipapi.ContentPartFileRef, FileData: "aGVsbG8=", FileName: "f.pdf"})).Validate(); err != nil {
		t.Fatalf("unexpected error for data-only file part: %v", err)
	}
}

func TestContentPartExtension_ClonePreservesData(t *testing.T) {
	t.Parallel()
	call := callWith(itemWith(lipapi.ContentPart{
		Kind: lipapi.ContentPartExtension,
		Extension: &lipapi.ExtensionContentPart{
			Namespace:   "acme",
			Type:        "acme:input_file",
			Implementor: "acme-vendor",
			Data:        json.RawMessage(`{"type":"acme:input_file","file_url":"https://x/f"}`),
		},
	}))
	cloned := lipapi.CloneCall(call)
	orig := call.Items[0].Content[0].Extension
	got := cloned.Items[0].Content[0].Extension
	if got == nil || got.Type != orig.Type || got.Namespace != orig.Namespace || got.Implementor != orig.Implementor || string(got.Data) != string(orig.Data) {
		t.Fatalf("clone did not preserve extension content part: %+v vs %+v", got, orig)
	}
	got.Data[0] = ' '
	if string(call.Items[0].Content[0].Extension.Data) == string(got.Data) {
		t.Fatal("clone shares extension data backing array")
	}
}

func TestContentPartExtension_WalkOpaqueData(t *testing.T) {
	t.Parallel()
	call := callWith(itemWith(extContentPart("acme:meta", `{"type":"acme:meta","v":1}`)))
	var fields []string
	if err := lipapi.WalkCallOpaqueData(call, func(field string, data []byte) error {
		fields = append(fields, field+"="+string(data))
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(fields) != 1 || !strings.Contains(fields[0], "acme:meta") {
		t.Fatalf("opaque walk did not visit extension content data: %v", fields)
	}
}

func TestContentPartExtension_RequiredCapabilities(t *testing.T) {
	t.Parallel()
	call := callWith(itemWith(extContentPart("acme:part", `{"type":"acme:part"}`)))
	req := lipapi.DeriveProtocolRequirements(call)
	if !slices.Contains(req.Capabilities, lipapi.CapabilityOpaqueExtensions) {
		t.Fatalf("expected CapabilityOpaqueExtensions in requirements, got %v", req.Capabilities)
	}
	required := lipapi.RequiredCapabilities(call)
	if !slices.Contains(required, lipapi.CapabilityOpaqueExtensions) {
		t.Fatalf("expected CapabilityOpaqueExtensions in RequiredCapabilities, got %v", required)
	}
}

func TestContentPartExtension_RequiredCapabilitiesFromToolResult(t *testing.T) {
	t.Parallel()
	call := callWith(lipapi.Item{
		Kind:   lipapi.ItemKindToolResult,
		ID:     "tr-1",
		Status: lipapi.ItemStatusCompleted,
		ToolResult: &lipapi.ToolResultItem{
			CallID: "call-1",
			Name:   "lookup",
			Parts:  []lipapi.ContentPart{extContentPart("acme:part", `{"type":"acme:part"}`)},
		},
	})
	required := lipapi.RequiredCapabilities(call)
	if !slices.Contains(required, lipapi.CapabilityOpaqueExtensions) {
		t.Fatalf("expected CapabilityOpaqueExtensions in RequiredCapabilities for tool-result extension part, got %v", required)
	}
	req := lipapi.DeriveProtocolRequirements(call)
	if !slices.Contains(req.Capabilities, lipapi.CapabilityOpaqueExtensions) {
		t.Fatalf("expected CapabilityOpaqueExtensions in requirements for tool-result extension part, got %v", req.Capabilities)
	}
}

func TestContentPartExtension_LegacyProjectionRejects(t *testing.T) {
	t.Parallel()
	call := callWith(itemWith(extContentPart("acme:part", `{"type":"acme:part"}`)))
	target := lipapi.LegacyProjectionTargetFromCaps(lipapi.NewBackendCaps(lipapi.CapabilityOrderedItems, lipapi.CapabilityOpaqueExtensions), lipapi.ReasoningReplaySupport{})
	if _, err := lipapi.ProjectItemsToLegacyView(call, target); err == nil {
		t.Fatal("expected legacy projection to reject opaque extension content part")
	} else if !lipapi.IsProjectionError(err) {
		t.Fatalf("expected ProjectionError, got: %T %v", err, err)
	}
}

func TestContentPartFileData_ValidationBounds(t *testing.T) {
	t.Parallel()
	part := lipapi.ContentPart{Kind: lipapi.ContentPartFileRef, FileData: strings.Repeat("a", lipapi.MaxPartTextBytes+1)}
	if err := callWith(itemWith(part)).Validate(); err == nil {
		t.Fatal("expected oversized FileData rejection")
	}
}
