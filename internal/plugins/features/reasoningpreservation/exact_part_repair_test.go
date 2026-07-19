package reasoningpreservation

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestCaptureExactReasoningPart_overflowMarksOversize(t *testing.T) {
	t.Parallel()
	o := &streamObserver{
		cfg: Config{State: StateConfig{MaxReasoningBytesPerTurn: 1024}},
		factory: &StreamObserverFactory{
			cfg: Config{State: StateConfig{MaxReasoningBytesPerTurn: 1024}},
			tel: NewTelemetry(),
		},
	}
	o.reasoningBytes = math.MaxInt - 8
	opaque := json.RawMessage(`{"k":"v"}`)
	if err := o.captureExactReasoningPartLocked(&lipapi.ReasoningPart{
		Dialect: lipapi.ReasoningDialectOpenAIResponsesItemV1,
		Opaque:  opaque,
	}); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if !o.oversized {
		t.Fatal("near-MaxInt reasoningBytes + add must mark oversize without wrapping")
	}
	if len(o.parts) != 0 {
		t.Fatal("overflow must not keep pending exact parts")
	}
	if o.reasoningBytes < 0 {
		t.Fatalf("reasoningBytes wrapped negative: %d", o.reasoningBytes)
	}
}

func TestValidateArtifacts_doesNotMutateOpaque(t *testing.T) {
	t.Parallel()
	opaque := json.RawMessage(`{"id":"rs_1","summary":[]}`)
	want := append(json.RawMessage(nil), opaque...)
	arts := []TurnArtifact{{
		ID:             "a1",
		ReasoningBytes: len(opaque),
		SourceBackend:  "be",
		SourceModel:    "m",
		Reasoning: []PlacedReasoning{{
			BeforeNonReasoningPart: 0,
			Part: lipapi.Part{
				Kind: lipapi.PartReasoning,
				Reasoning: &lipapi.ReasoningPart{
					Dialect: lipapi.ReasoningDialectOpenAIResponsesItemV1,
					Opaque:  opaque,
				},
			},
		}},
	}}
	if err := validateArtifacts(arts); err != nil {
		t.Fatalf("validateArtifacts: %v", err)
	}
	if !bytes.Equal(arts[0].Reasoning[0].Part.Reasoning.Opaque, want) {
		t.Fatal("validateArtifacts must not mutate stored Opaque")
	}
}
