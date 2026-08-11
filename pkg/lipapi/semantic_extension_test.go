package lipapi_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestSemanticExtension_PreservesPresenceAndCloneOwnership(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hello"}}}},
		SemanticExtensions: []lipapi.SemanticExtension{
			{Namespace: "lip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: lipapi.SemanticExtensionValue, Data: json.RawMessage(`"cache-1"`)},
			{Namespace: "lip", Type: "nullable_hint", Implementor: "proxy", Direction: "request", Presence: lipapi.SemanticExtensionNull},
		},
	}
	if err := call.Validate(); err != nil {
		t.Fatal(err)
	}
	clone := lipapi.CloneCall(call)
	clone.SemanticExtensions[0].Data[0] = 'X'
	if string(call.SemanticExtensions[0].Data) != `"cache-1"` {
		t.Fatalf("semantic extension data was aliased: %s", call.SemanticExtensions[0].Data)
	}
	if clone.SemanticExtensions[1].Presence != lipapi.SemanticExtensionNull || len(clone.SemanticExtensions[1].Data) != 0 {
		t.Fatalf("null presence was not preserved: %#v", clone.SemanticExtensions[1])
	}
}

func TestSemanticExtension_ExactRequirementsAndAdmission(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages:           []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hello"}}}},
		SemanticExtensions: []lipapi.SemanticExtension{{Namespace: "lip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: lipapi.SemanticExtensionValue, Data: json.RawMessage(`"cache-1"`)}},
	}
	required := lipapi.DeriveProtocolRequirements(call)
	if len(required.ExtensionTypes) != 1 || required.ExtensionTypes[0].Type != "prompt_cache_key" {
		t.Fatalf("requirements=%+v", required)
	}
	if result := lipapi.MatchRequirements(required, lipapi.ProtocolRequirements{}, lipapi.ReasoningReplaySupport{}); result.Kind == lipapi.NegotiationLossless {
		t.Fatal("expected unsupported semantic carrier identity to fail admission")
	}
	if result := lipapi.MatchRequirements(required, lipapi.ProtocolRequirements{
		Capabilities:   []lipapi.Capability{lipapi.CapabilityOpaqueExtensions},
		ExtensionTypes: required.ExtensionTypes,
	}, lipapi.ReasoningReplaySupport{}); result.Kind != lipapi.NegotiationLossless {
		t.Fatalf("expected exact identity admission to remain lossless: %+v", result)
	}
}

func TestSemanticExtension_RejectsOversizeAndInvalidPresence(t *testing.T) {
	t.Parallel()
	base := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hello"}}}}}
	for name, ext := range map[string]lipapi.SemanticExtension{
		"missing identity": {Presence: lipapi.SemanticExtensionValue, Data: json.RawMessage(`true`)},
		"invalid json":     {Namespace: "lip", Type: "x", Presence: lipapi.SemanticExtensionValue, Data: json.RawMessage("{")},
		"oversize":         {Namespace: "lip", Type: "x", Presence: lipapi.SemanticExtensionValue, Data: json.RawMessage(strings.Repeat("x", lipapi.MaxSemanticExtensionDataBytes+1))},
	} {
		t.Run(name, func(t *testing.T) {
			call := lipapi.CloneCall(base)
			call.SemanticExtensions = []lipapi.SemanticExtension{ext}
			if err := call.Validate(); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestSemanticExtension_RejectsUnboundedIdentityDuplicateAndEnvelope(t *testing.T) {
	t.Parallel()
	base := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hello"}}}}}
	for name, ext := range map[string]lipapi.SemanticExtension{
		"closed direction": {Namespace: "lip", Type: "x", Implementor: "proxy", Direction: "other", Presence: lipapi.SemanticExtensionNull},
		"envelope":         {Namespace: "lip", Type: "x", Implementor: "proxy", Direction: "request", Presence: lipapi.SemanticExtensionValue, Data: json.RawMessage(`{"request":{"messages":[]}}`)},
	} {
		t.Run(name, func(t *testing.T) {
			call := lipapi.CloneCall(base)
			call.SemanticExtensions = []lipapi.SemanticExtension{ext}
			if err := call.Validate(); err == nil {
				t.Fatal("expected semantic carrier validation failure")
			}
		})
	}
	duplicate := lipapi.CloneCall(base)
	duplicate.SemanticExtensions = []lipapi.SemanticExtension{
		{Namespace: "lip", Type: "x", Implementor: "proxy", Direction: "request", Presence: lipapi.SemanticExtensionNull},
		{Namespace: "lip", Type: "x", Implementor: "proxy", Direction: "request", Presence: lipapi.SemanticExtensionNull},
	}
	if err := duplicate.Validate(); err == nil {
		t.Fatal("expected duplicate semantic carrier identity rejection")
	}
}

func TestSemanticExtension_DoesNotBecomeLegacyProjectionTunnel(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Items:              []lipapi.Item{{Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "hello"}}}},
		SemanticExtensions: []lipapi.SemanticExtension{{Namespace: "lip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: lipapi.SemanticExtensionValue, Data: json.RawMessage(`"cache-1"`)}},
	}
	_, err := lipapi.ProjectItemsToLegacyView(call, lipapi.DefaultLegacyProjectionTarget(lipapi.NewBackendCaps(lipapi.CapabilityOpaqueExtensions), lipapi.ReasoningReplaySupport{}))
	if err == nil || !lipapi.IsProjectionError(err) {
		t.Fatalf("expected semantic residual to be rejected, got %v", err)
	}
}
