package backendplugin_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestApplyOrderedItemWire_preservesJSONAndToolResultContentKinds(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Items: []lipapi.Item{{
			Kind:   lipapi.ItemKindMessage,
			ID:     "msg-1",
			Status: lipapi.ItemStatusCompleted,
			Role:   lipapi.RoleAssistant,
			Content: []lipapi.ContentPart{
				{Kind: lipapi.ContentPartJSON, Text: `{"a":1}`},
				{Kind: lipapi.ContentPartToolResult, Text: "72F"},
			},
		}},
	}
	inv := backendplugin.Invocation{
		RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: new("x")}}}},
	}
	backendplugin.ApplyOrderedItemWire(&inv, call)
	if len(inv.Items) != 1 || len(inv.Items[0].Content) != 2 {
		t.Fatalf("items=%#v", inv.Items)
	}
	if inv.Items[0].Content[0].Kind != backendplugin.PartKindJSON {
		t.Fatalf("json kind=%q", inv.Items[0].Content[0].Kind)
	}
	if inv.Items[0].Content[1].Kind != backendplugin.PartKindToolResult {
		t.Fatalf("tool_result kind=%q", inv.Items[0].Content[1].Kind)
	}
}

func TestApplyOrderedItemWire_firstClassFields(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenResponsesCreate,
			DeliveryMode: lipapi.DeliveryModeStreaming,
		},
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
		RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: new("hello")}}}},
	}
	backendplugin.ApplyCallWireMetadata(&inv, call, nil)
	if !backendplugin.HasItemAuthorityInvocation(inv) {
		t.Fatal("expected item authority on invocation")
	}
	req, ok := backendplugin.ProtocolRequirementsFromInvocation(inv)
	if !ok || !containsCap(req.Capabilities, lipapi.CapabilityOrderedItems) {
		t.Fatalf("requirements=%#v ok=%v", req, ok)
	}
	if inv.Operation != string(lipapi.OperationOpenResponsesCreate) {
		t.Fatalf("operation=%q", inv.Operation)
	}
}

func TestInvocationToProto_roundTripsOrderedItems(t *testing.T) {
	t.Parallel()

	out := "72F"
	inv := backendplugin.Invocation{
		RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
		ItemAuthority: true,
		Operation:     string(lipapi.OperationOpenResponsesCreate),
		DeliveryMode:  string(lipapi.DeliveryModeStreaming),
		Items: []backendplugin.InvocationItem{
			{
				Kind: "tool_call", ID: "tc-1", Status: "completed",
				ToolCall: &backendplugin.InvocationToolCall{
					CallID: "c1", Name: "fn", Arguments: backendplugin.RawJSONFromBytes([]byte(`{}`)),
				},
			},
			{
				Kind: "tool_result", ID: "tr-1", Status: "completed",
				ToolResult: &backendplugin.InvocationToolResult{CallID: "c1", Name: "fn", Output: &out},
			},
		},
		ProtocolRequirements: backendplugin.ProtocolRequirements{
			Capabilities: []string{string(lipapi.CapabilityOrderedItems)},
		},
	}
	p, err := backendplugin.InvocationToProto(inv)
	if err != nil {
		t.Fatal(err)
	}
	back, err := backendplugin.InvocationFromProto(p)
	if err != nil {
		t.Fatal(err)
	}
	if !back.ItemAuthority || len(back.Items) != 2 || back.Items[1].ToolResult == nil {
		t.Fatalf("back=%#v", back)
	}
}
