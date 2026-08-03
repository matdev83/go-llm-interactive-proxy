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

func TestEventToLipapi_preservesExactReasoningFields(t *testing.T) {
	t.Parallel()

	dialect := string(lipapi.ReasoningDialectOpenAIResponsesItemV1)
	summary := json.RawMessage(`[{"type":"summary_text","text":"s"}]`)
	content := json.RawMessage(`[{"type":"output_text","text":"t"}]`)
	dto := backendplugin.CanonicalEvent{
		Kind:                      backendplugin.EventReasoningPart,
		ReasoningDialect:          &dialect,
		ReasoningSummary:          backendplugin.RawJSONFromBytes(summary),
		ReasoningContent:          backendplugin.RawJSONFromBytes(content),
		ReasoningEncryptedContent: backendplugin.RawJSONNullValue(),
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
	rp := event.Reasoning
	if rp == nil {
		t.Fatal("reasoning is nil")
	}
	if !rp.SummaryPresent || string(rp.Summary) != string(summary) {
		t.Fatalf("summary=%s present=%v", rp.Summary, rp.SummaryPresent)
	}
	if !rp.ContentPresent || string(rp.Content) != string(content) {
		t.Fatalf("content=%s present=%v", rp.Content, rp.ContentPresent)
	}
	if !rp.EncryptedContentPresent || string(rp.EncryptedContent) != "null" {
		t.Fatalf("encrypted_content=%s present=%v", rp.EncryptedContent, rp.EncryptedContentPresent)
	}
}
