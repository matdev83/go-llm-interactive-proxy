package backendplugin_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestApplyItemAuthorityMetadata_usesFirstClassFields(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Items: []lipapi.Item{{
			Kind:   lipapi.ItemKindMessage,
			ID:     "msg-1",
			Status: lipapi.ItemStatusCompleted,
			Role:   lipapi.RoleUser,
			Content: []lipapi.ContentPart{
				{Kind: lipapi.ContentPartText, Text: "hello"},
			},
		}},
	}
	inv := backendplugin.Invocation{
		RequestID: "req-1", AttemptID: "att-1", ALegID: "a", BLegID: "b", CanonicalModelID: "model",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: new("hello")}}}},
	}
	backendplugin.ApplyCallWireMetadata(&inv, call, nil)
	if !backendplugin.HasItemAuthorityInvocation(inv) {
		t.Fatal("expected item authority invocation")
	}
	req, ok := backendplugin.ProtocolRequirementsFromInvocation(inv)
	if !ok {
		t.Fatal("expected protocol requirements")
	}
	if !containsCap(req.Capabilities, lipapi.CapabilityOrderedItems) {
		t.Fatalf("requirements=%#v", req)
	}
}

func TestProtocolRequirementsFromMetadata_invalidJSON(t *testing.T) {
	t.Parallel()

	_, ok := backendplugin.ProtocolRequirementsFromMetadata(map[string]string{
		backendplugin.MetaProtocolRequirements: "{",
	})
	if ok {
		t.Fatal("expected decode failure")
	}
}

func TestCapabilitySummaryFromLipapi_mapsDialectCapabilities(t *testing.T) {
	t.Parallel()

	summary := backendplugin.CapabilitySummaryFromLipapi(lipapi.NewBackendCaps(
		lipapi.CapabilityTools,
		lipapi.CapabilityReasoningReplay,
	))
	if !summary.Tools || !summary.ReasoningReplay {
		t.Fatalf("summary=%#v", summary)
	}
}

func TestApplyItemAuthorityMetadata_noOpForLegacyAuthority(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}},
	}
	inv := backendplugin.Invocation{}
	backendplugin.ApplyItemAuthorityMetadata(&inv, call)
	if backendplugin.HasItemAuthorityInvocation(inv) {
		t.Fatal("legacy authority must not set item authority")
	}
	_ = json.RawMessage(`{}`)
}

func containsCap(caps []lipapi.Capability, want lipapi.Capability) bool {
	return slices.Contains(caps, want)
}
