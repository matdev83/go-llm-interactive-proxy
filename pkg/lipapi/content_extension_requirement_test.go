package lipapi_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestContentPartExtension_DerivesExactExtensionRequirement(t *testing.T) {
	t.Parallel()
	call := callWith(itemWith(extContentPart("acme:input_file", `{"type":"acme:input_file","file_url":"https://x/f"}`)))
	req := lipapi.DeriveProtocolRequirements(call)
	want := lipapi.ExtensionRequirement{Namespace: "acme", Type: "acme:input_file"}
	if !slices.Contains(req.ExtensionTypes, want) {
		t.Fatalf("expected extension requirement %+v in %+v", want, req.ExtensionTypes)
	}
	if !slices.Contains(req.Capabilities, lipapi.CapabilityOpaqueExtensions) {
		t.Fatalf("expected opaque_extensions capability, got %v", req.Capabilities)
	}
}

func TestContentPartExtension_DerivesRequirementFromStructuredToolResultPart(t *testing.T) {
	t.Parallel()
	call := callWith(lipapi.Item{
		Kind:   lipapi.ItemKindToolResult,
		ID:     "tr-1",
		Status: lipapi.ItemStatusCompleted,
		ToolResult: &lipapi.ToolResultItem{
			CallID: "call-1",
			Name:   "lookup",
			Parts:  []lipapi.ContentPart{extContentPart("acme.com/part", `{"type":"acme.com/part","payload":{"k":1}}`)},
		},
	})
	req := lipapi.DeriveProtocolRequirements(call)
	want := lipapi.ExtensionRequirement{Namespace: "acme.com", Type: "acme.com/part"}
	if !slices.Contains(req.ExtensionTypes, want) {
		t.Fatalf("expected tool-result extension requirement %+v in %+v", want, req.ExtensionTypes)
	}
}

func TestContentPartExtension_CanonicalMetadataIsUsedForNonWireSources(t *testing.T) {
	t.Parallel()
	part := lipapi.ContentPart{
		Kind: lipapi.ContentPartExtension,
		Extension: &lipapi.ExtensionContentPart{
			Namespace:   "custom",
			Type:        "acme:widget",
			Implementor: "acme-vendor",
			Data:        json.RawMessage(`{"type":"acme:widget"}`),
		},
	}
	req := lipapi.DeriveProtocolRequirements(callWith(itemWith(part)))
	want := lipapi.ExtensionRequirement{Namespace: "custom", Type: "acme:widget", Implementor: "acme-vendor"}
	if !slices.Contains(req.ExtensionTypes, want) {
		t.Fatalf("expected carried extension requirement %+v in %+v", want, req.ExtensionTypes)
	}
}

func TestContentPartExtension_DeriveExtensionNamespace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		typ  string
		want string
	}{
		{typ: "acme:input_file", want: "acme"},
		{typ: "acme.com/part", want: "acme.com"},
		{typ: "a:b/c", want: "a"},
		{typ: "", want: ""},
		{typ: "unprefixed", want: "unprefixed"},
	}
	for _, tt := range tests {
		if got := lipapi.DeriveExtensionNamespace(tt.typ); got != tt.want {
			t.Fatalf("DeriveExtensionNamespace(%q) = %q, want %q", tt.typ, got, tt.want)
		}
	}
}

func TestContentPartExtension_ValidationNamespaceAndImplementor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ext     *lipapi.ExtensionContentPart
		wantErr string
	}{
		{
			name: "valid namespace and implementor",
			ext: &lipapi.ExtensionContentPart{
				Namespace:   "acme",
				Type:        "acme:part",
				Implementor: "acme-vendor",
				Data:        json.RawMessage(`{"type":"acme:part"}`),
			},
		},
		{
			name: "namespace with whitespace rejected",
			ext: &lipapi.ExtensionContentPart{
				Namespace: "ac me",
				Type:      "acme:part",
				Data:      json.RawMessage(`{"type":"acme:part"}`),
			},
			wantErr: "namespace must not contain whitespace",
		},
		{
			name: "oversized namespace rejected",
			ext: &lipapi.ExtensionContentPart{
				Namespace: strings.Repeat("a", lipapi.MaxExtensionNamespaceBytes+1),
				Type:      "acme:part",
				Data:      json.RawMessage(`{"type":"acme:part"}`),
			},
			wantErr: "exceeds",
		},
		{
			name: "oversized implementor rejected",
			ext: &lipapi.ExtensionContentPart{
				Implementor: strings.Repeat("b", lipapi.MaxExtensionImplementorBytes+1),
				Type:        "acme:part",
				Data:        json.RawMessage(`{"type":"acme:part"}`),
			},
			wantErr: "exceeds",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := callWith(itemWith(lipapi.ContentPart{Kind: lipapi.ContentPartExtension, Extension: tt.ext})).Validate()
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

func TestContentPartExtension_JSONRoundTripPreservesNamespaceAndImplementor(t *testing.T) {
	t.Parallel()
	part := lipapi.ContentPart{
		Kind: lipapi.ContentPartExtension,
		Extension: &lipapi.ExtensionContentPart{
			Namespace:   "acme",
			Type:        "acme:part",
			Implementor: "acme-vendor",
			Data:        json.RawMessage(`{"type":"acme:part","payload":{"k":1}}`),
		},
	}
	call := callWith(itemWith(part))
	raw, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	var back lipapi.Call
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got := back.Items[0].Content[0].Extension
	if got == nil || got.Namespace != "acme" || got.Type != "acme:part" || got.Implementor != "acme-vendor" {
		t.Fatalf("json round-trip mismatch: %+v", got)
	}
	if string(got.Data) != `{"type":"acme:part","payload":{"k":1}}` {
		t.Fatalf("json round-trip data mismatch: %s", string(got.Data))
	}
}

func TestAdmitCandidate_ContentPartExtensionRequiresExactDeclaredType(t *testing.T) {
	t.Parallel()
	call := callWith(itemWith(extContentPart("acme:part", `{"type":"acme:part"}`)))
	call.Invocation = lipapi.Invocation{
		Operation:     lipapi.OperationOpenResponsesCreate,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
	caps := lipapi.NewBackendCaps(defaultOrderedCaps()...)
	base := func() lipapi.CandidateAdmissionInput {
		return lipapi.CandidateAdmissionInput{
			Call:           call,
			Invocation:     call.Invocation,
			BackendCaps:    caps,
			TransportCaps:  lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{Operation: lipapi.OperationOpenResponsesCreate, Modes: []lipapi.TransportMode{lipapi.TransportModeNonStreaming}}),
			ReplaySupport:  lipapi.ReasoningReplaySupport{},
			DialectSupport: lipapi.DialectSupport{},
			ProjectionTarget: lipapi.DefaultLegacyProjectionTarget(
				caps, lipapi.ReasoningReplaySupport{}),
		}
	}

	// Capability is declared but no exact extension type: admission must reject
	// before any upstream work.
	without := base()
	res := lipapi.AdmitCandidate(without)
	if res.Kind != lipapi.NegotiationReject {
		t.Fatalf("expected reject without declared extension type, got %s", res.Kind)
	}
	if res.Requirements.Kind != lipapi.NegotiationReject {
		t.Fatalf("expected requirements reject, got %s", res.Requirements.Kind)
	}
	if len(res.Requirements.MissingExtensions) == 0 {
		t.Fatalf("expected a missing extension requirement, got %+v", res.Requirements)
	}

	// Declaring the exact namespace/type satisfies admission losslessly.
	with := base()
	with.DialectSupport = lipapi.DialectSupport{
		ExtensionTypes: []lipapi.ExtensionRequirement{{Namespace: "acme", Type: "acme:part"}},
	}
	res = lipapi.AdmitCandidate(with)
	if res.Kind != lipapi.NegotiationLossless {
		t.Fatalf("expected lossless with declared extension type, got %s err=%v", res.Kind, res.Err())
	}

	// A mismatched declared type still rejects.
	mismatch := base()
	mismatch.DialectSupport = lipapi.DialectSupport{
		ExtensionTypes: []lipapi.ExtensionRequirement{{Namespace: "other", Type: "other:part"}},
	}
	res = lipapi.AdmitCandidate(mismatch)
	if res.Kind != lipapi.NegotiationReject {
		t.Fatalf("expected reject with mismatched declared extension type, got %s", res.Kind)
	}
}

func defaultOrderedCaps() []lipapi.Capability {
	return []lipapi.Capability{
		lipapi.CapabilityStreaming,
		lipapi.CapabilityTools,
		lipapi.CapabilityVision,
		lipapi.CapabilityDocuments,
		lipapi.CapabilityReasoning,
		lipapi.CapabilityParallelToolCalls,
		lipapi.CapabilityOrderedItems,
		lipapi.CapabilityAssistantPhase,
		lipapi.CapabilityItemReferences,
		lipapi.CapabilityCompaction,
		lipapi.CapabilityOpaqueExtensions,
	}
}
