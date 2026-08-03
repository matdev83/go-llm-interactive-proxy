package backendplugin_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func orderedItemNegotiation() backendplugin.Negotiation {
	return backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
		EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
	}
}

func invocationFor(call lipapi.Call) backendplugin.Invocation {
	return backendplugin.Invocation{
		RequestID:        "req",
		AttemptID:        "att",
		ALegID:           "a",
		BLegID:           "b",
		CanonicalModelID: "m",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: func() *string { s := "x"; return &s }()}},
		}},
	}
}

func TestABI_rejectsOpaqueExtensionContentPartBeforeExecution(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{Items: []lipapi.Item{{
		Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser,
		Content: []lipapi.ContentPart{{
			Kind: lipapi.ContentPartExtension,
			Extension: &lipapi.ExtensionContentPart{
				Type: "acme:input_file",
				Data: json.RawMessage(`{"type":"acme:input_file","file_url":"https://x/f"}`),
			},
		}},
	}}}
	inv := invocationFor(call)
	err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, orderedItemNegotiation())
	if !errors.Is(err, backendplugin.ErrExactOpenResponsesUnsupported) {
		t.Fatalf("expected fail-closed exact OpenResponses rejection on old minor, got %v", err)
	}
}

func TestABI_rejectsInlineFileDataBeforeExecution(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{Items: []lipapi.Item{{
		Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser,
		Content: []lipapi.ContentPart{{
			Kind:     lipapi.ContentPartFileRef,
			FileData: "aGVsbG8=",
			FileName: "minimal.pdf",
		}},
	}}}
	inv := invocationFor(call)
	err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, orderedItemNegotiation())
	if !errors.Is(err, backendplugin.ErrExactOpenResponsesUnsupported) {
		t.Fatalf("expected fail-closed exact OpenResponses rejection on old minor, got %v", err)
	}
}

func TestABI_rejectsToolResultExtensionContentPart(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{Items: []lipapi.Item{{
		Kind: lipapi.ItemKindToolResult, ID: "tr1", Status: lipapi.ItemStatusCompleted,
		ToolResult: &lipapi.ToolResultItem{
			CallID: "c1",
			Name:   "fn",
			Parts: []lipapi.ContentPart{{
				Kind: lipapi.ContentPartExtension,
				Extension: &lipapi.ExtensionContentPart{
					Type: "acme:part",
					Data: json.RawMessage(`{"type":"acme:part"}`),
				},
			}},
		},
	}}}
	inv := invocationFor(call)
	err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, orderedItemNegotiation())
	if !errors.Is(err, backendplugin.ErrExactOpenResponsesUnsupported) {
		t.Fatalf("expected fail-closed exact OpenResponses rejection on old minor, got %v", err)
	}
}

func TestABI_preservesFileRefWithoutFileData(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{Items: []lipapi.Item{{
		Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser,
		Content: []lipapi.ContentPart{{
			Kind:     lipapi.ContentPartFileRef,
			FileRef:  "https://x/report.pdf",
			FileMIME: "application/pdf",
			FileName: "report.pdf",
		}},
	}}}
	inv := invocationFor(call)
	if err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, orderedItemNegotiation()); err != nil {
		t.Fatalf("unexpected rejection of file_ref part: %v", err)
	}
	if len(inv.Items) != 1 || len(inv.Items[0].Content) != 1 {
		t.Fatalf("items=%#v", inv.Items)
	}
	if inv.Items[0].Content[0].Kind != backendplugin.PartKindFileRef || inv.Items[0].Content[0].FileRef == nil || *inv.Items[0].Content[0].FileRef != "https://x/report.pdf" {
		t.Fatalf("file_ref part not preserved: %#v", inv.Items[0].Content[0])
	}
}

func TestABI_preservesInlineFileDataAndExtensionAtMinor3(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{Items: []lipapi.Item{{
		Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser,
		Content: []lipapi.ContentPart{
			{Kind: lipapi.ContentPartFileRef, FileData: "aGVsbG8=", FileName: "minimal.pdf"},
			{
				Kind:      lipapi.ContentPartExtension,
				Extension: &lipapi.ExtensionContentPart{Type: "acme:input_file", Data: json.RawMessage(`{"type":"acme:input_file"}`)},
			},
		},
	}}}
	inv := invocationFor(call)
	if err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, exactNegotiation()); err != nil {
		t.Fatalf("minor-3 exact call rejected: %v", err)
	}
	if len(inv.Items) != 1 || len(inv.Items[0].Content) != 2 {
		t.Fatalf("items=%#v", inv.Items)
	}
	if inv.Items[0].Content[0].FileData == nil || *inv.Items[0].Content[0].FileData != "aGVsbG8=" {
		t.Fatalf("inline file_data not preserved: %#v", inv.Items[0].Content[0])
	}
	if inv.Items[0].Content[1].Kind != backendplugin.PartKindExtension || inv.Items[0].Content[1].ExtensionType == nil {
		t.Fatalf("extension part not preserved: %#v", inv.Items[0].Content[1])
	}
}

func TestInvocation_ValidateRejectsUnspecifiedContentKindSafetyNet(t *testing.T) {
	t.Parallel()
	inv := backendplugin.Invocation{
		RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
		ItemAuthority: true,
		Items: []backendplugin.InvocationItem{{
			Kind: "message", ID: "m1", Status: "completed", Role: backendplugin.RoleUser,
			Content: []backendplugin.InvocationContentPart{{Kind: backendplugin.PartKind("extension")}},
		}},
		ProtocolRequirements: backendplugin.ProtocolRequirementsDTO{
			Capabilities: []string{string(lipapi.CapabilityOrderedItems), string(lipapi.CapabilityOpaqueExtensions)},
		},
	}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected Validate to reject unsupported content part kind")
	}
}
