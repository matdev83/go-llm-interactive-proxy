package adapter

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestEventToLipapi_ExactReasoningPart(t *testing.T) {
	t.Parallel()

	dialect := string(lipapi.ReasoningDialectOpenAIResponsesItemV1)
	opaque := json.RawMessage(`{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"encrypted-state"}`)
	dto := backendplugin.CanonicalEvent{
		Kind:             backendplugin.EventReasoningPart,
		ReasoningDialect: &dialect,
		ReasoningOpaque:  opaque,
	}
	wire, err := backendplugin.CanonicalEventToProto(&dto)
	if err != nil {
		t.Fatal(err)
	}
	back, err := backendplugin.CanonicalEventFromProto(wire)
	if err != nil {
		t.Fatal(err)
	}
	event, err := eventToLipapi(back)
	if err != nil {
		t.Fatal(err)
	}
	if event.Reasoning == nil || event.Reasoning.Dialect != lipapi.ReasoningDialectOpenAIResponsesItemV1 {
		t.Fatalf("reasoning = %+v", event.Reasoning)
	}
	if string(event.Reasoning.Opaque) != string(opaque) {
		t.Fatalf("opaque = %s, want %s", event.Reasoning.Opaque, opaque)
	}
}
