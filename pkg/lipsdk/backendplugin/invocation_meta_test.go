package backendplugin_test

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestApplyRestoreCallWireMetadata_roundTrip(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIResponses,
			DeliveryMode:  lipapi.DeliveryModeStreaming,
			TransportMode: lipapi.TransportModeStreaming,
		},
		Extensions: map[string]json.RawMessage{
			"openrouter.provider": json.RawMessage(`{"order":["a"]}`),
			"openai.extra_body.x": json.RawMessage(`1`),
		},
	}
	inv := backendplugin.Invocation{}
	backendplugin.ApplyCallWireMetadata(&inv, call, map[string]string{"provider": "sambanova"})
	if inv.SafeMetadata[backendplugin.MetaOperation] != string(lipapi.OperationOpenAIResponses) {
		t.Fatalf("op=%q", inv.SafeMetadata[backendplugin.MetaOperation])
	}
	if backendplugin.RouteParam(inv.SafeMetadata, "provider") != "sambanova" {
		t.Fatalf("provider=%q", backendplugin.RouteParam(inv.SafeMetadata, "provider"))
	}
	out, err := backendplugin.CallFromInvocation(backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg",
		CanonicalModelID: "m", Messages: []backendplugin.Message{{
			Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: new("hi")}},
		}},
		SafeMetadata: inv.SafeMetadata,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Invocation.Operation != lipapi.OperationOpenAIResponses {
		t.Fatalf("restored op=%q", out.Invocation.Operation)
	}
	if string(out.Extensions["openrouter.provider"]) != `{"order":["a"]}` {
		t.Fatalf("ext=%v", out.Extensions)
	}
	_ = url.Values{}
}

func strPtr(s string) *string { return new(s) }
