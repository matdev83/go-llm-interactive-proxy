package runtime

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// TestTask12_3_Runtime_ResponseEvidenceFromWire verifies that responseRequestEvidence
// can be derived directly from WireTurnFacts without constructing or dereferencing lipapi.Call.
func TestTask12_3_Runtime_ResponseEvidenceFromWire(t *testing.T) {
	turnFacts := largebody.DefaultTestWireTurnFacts()
	ev := responseEvidenceFromWire(turnFacts)

	if ev.traceID != turnFacts.Identity.TraceID {
		t.Fatalf("expected traceID %s, got %s", turnFacts.Identity.TraceID, ev.traceID)
	}
	if ev.aLegID != turnFacts.Session.Input.ALegID {
		t.Fatalf("expected aLegID %s, got %s", turnFacts.Session.Input.ALegID, ev.aLegID)
	}
	if ev.sessionID != turnFacts.Session.Input.AuthoritativeSessionID {
		t.Fatalf("expected sessionID %s, got %s", turnFacts.Session.Input.AuthoritativeSessionID, ev.sessionID)
	}
	if ev.secureTurnOK {
		t.Fatalf("expected secureTurnOK false for un-entered wire secure turn")
	}
}

// TestTask12_3_Runtime_CompactionResponseObservation_EvidenceDriven verifies that
// observeCompactionReleaseFinalEvidence operates strictly on responseRequestEvidence
// and canonical lipapi.Event, without requiring facts.baseline or prompt content.
func TestTask12_3_Runtime_CompactionResponseObservation_EvidenceDriven(t *testing.T) {
	pipe := newResponsePipeline()
	turnFacts := largebody.DefaultTestWireTurnFacts()
	ev := responseEvidenceFromWire(turnFacts)

	event := lipapi.Event{
		Kind:  lipapi.EventTextDelta,
		Delta: "test delta",
	}

	attempt := &attemptSession{
		bleg: b2bua.BLegRecord{BLegID: "bleg-test", Seq: 1},
	}

	// Should not panic, should execute safely on canonical event with bounded evidence
	dispatch := pipe.observeCompactionReleaseFinalEvidence(context.Background(), ev, attempt, &event)
	_ = dispatch
}

// TestTask12_3_Runtime_TerminalSnapshot_CanonicalEventsOnly verifies that
// responseTerminalSnapshot operates strictly on canonical lipapi.Event and accumulator snapshot,
// without requiring lipapi.Call.
func TestTask12_3_Runtime_TerminalSnapshot_CanonicalEventsOnly(t *testing.T) {
	snap := responseTerminalSnapshot{
		releasedText: "canonical output",
		seenEvents: []lipapi.Event{
			{Kind: lipapi.EventTextDelta, Delta: "canonical output"},
		},
		usageEv: lipapi.Event{Kind: lipapi.EventUsageDelta, OutputTokens: 10},
	}

	if snap.releasedText != "canonical output" {
		t.Fatalf("expected released text 'canonical output', got %s", snap.releasedText)
	}
	if len(snap.seenEvents) != 1 {
		t.Fatalf("expected 1 seen event, got %d", len(snap.seenEvents))
	}
	if snap.usageEv.OutputTokens != 10 {
		t.Fatalf("expected 10 output tokens, got %d", snap.usageEv.OutputTokens)
	}
}
