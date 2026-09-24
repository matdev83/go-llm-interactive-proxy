package runtime

// F4 adversarial repair: the pre-terminal economic checkpoint queue and the
// local boundary head map are separate owners. A valid local boundary head can
// already be durable while every ordinary pending/deferred queue slot is full.
// When the terminal drain then observes a changed local measurement, queue
// admission rejects it. The old code republished the stale prior head as if it
// were the final measurement and recorded no capture loss, so the sealed
// record silently carried a stale local value even though a later unrelated
// flush succeeded.
//
// The production direct terminal branch (TerminalizeAttempt without
// BillingLegFn) is the entrypoint under test: it drains local observations into
// the terminal record, flushes the checkpoint queue, then builds the record.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

const f4CallID = billing.BillingCallID("bc_0123456789abcdef0123456789abcdef")

// f4LocalBoundaryAttempt builds an attempt whose direct TerminalizeAttempt
// branch owns the terminal billing record (no BillingLegFn), with a live local
// boundary accumulator and a toggleable durable observation sink.
func f4LocalBoundaryAttempt(sink metering.ObservationSink) (*attemptSession, *coremetering.BoundaryAccumulator) {
	boundary := coremetering.NewBoundaryAccumulator()
	boundary.PrepareCall(lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("input")}}}})
	boundary.MarkAttempted()
	boundary.MarkAccepted(true)
	session := &attemptSession{
		terminal:        newStreamTerminal(sdkterminal.ScopeAttempt),
		boundary:        boundary,
		observationSink: sink,
		bleg:            b2bua.BLegRecord{ALegID: "a-f4", BLegID: "b-f4", Seq: 1},
		cand:            routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-f4", Model: "model-f4"}},
		billingCallID:   f4CallID,
		billingStoreID:  "store-f4",
		requestID:       "request-f4",
		now:             func() time.Time { return time.Unix(1_700_000_100, 0).UTC() },
		billingEnabled:  func() bool { return true },
	}
	return session, boundary
}

// f4ObserveProviderOutput appends a bounded text delta so the local provider
// output plane accumulates byteCount text bytes.
func f4ObserveProviderOutput(boundary *coremetering.BoundaryAccumulator, byteCount int) {
	boundary.ObserveProviderEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: strings.Repeat("x", byteCount)})
}

// f4SaturateOrdinaryCheckpointQueue fills every pending and deferred slot with
// a unique, non-coalescing accepted delta observation.
func f4SaturateOrdinaryCheckpointQueue(t *testing.T, session *attemptSession) {
	t.Helper()
	total := maxPreTerminalEconomicCheckpointPending + maxPreTerminalEconomicCheckpointDeferred
	for i := uint64(1); i <= uint64(total); i++ {
		observation := refinement41Observation(i, "1")
		observation.Semantics = metering.SemanticsDelta
		if admission := session.rememberEconomicEvidenceOnce(execbackend.EconomicEvidence{Observation: observation}); admission.disposition == economicEvidenceAdmissionRejected {
			t.Fatalf("ordinary checkpoint %d rejected before saturation: %+v", i, admission)
		}
	}
	session.checkpointMu.Lock()
	queued := len(session.checkpointPending) + len(session.checkpointDeferred)
	session.checkpointMu.Unlock()
	if queued != total {
		t.Fatalf("saturated checkpoint queue entries=%d, want %d", queued, total)
	}
}

// f4LocalProviderOutputTokens returns the coefficient of the local
// provider-output text-token measure in the terminal record.
func f4LocalProviderOutputTokens(t *testing.T, record billing.CallLegUsageRecord) string {
	t.Helper()
	if tokens, ok := f4ObservationsLocalProviderOutputTokens(record.Observations); ok {
		return tokens
	}
	t.Fatalf("terminal record has no local provider-output token observation: %+v", record.Observations)
	return ""
}

// f4ObservationsLocalProviderOutputTokens extracts the local provider-output
// text-token coefficient from a set of observations.
func f4ObservationsLocalProviderOutputTokens(observations []metering.Observation) (string, bool) {
	for _, observation := range observations {
		if observation.Origin != metering.OriginLocal || observation.Boundary != metering.BoundaryBackendIngress {
			continue
		}
		for _, measure := range observation.Measures {
			if measure.Key.Direction == metering.DirectionOutput && measure.Key.Component == metering.ComponentTextToken && measure.Value != nil {
				return measure.Value.Coefficient, true
			}
		}
	}
	return "", false
}

// f4HasCheckpointCapacityLossMarker reports whether the record carries the
// runtime-owned reserved capture-loss marker for checkpoint-capacity loss.
func f4HasCheckpointCapacityLossMarker(record billing.CallLegUsageRecord) bool {
	wantReason := evidenceCaptureLossConflictReasonPrefix + evidenceCaptureLossCheckpointCapacity.reason()
	for _, conflict := range record.EvidenceConflicts {
		if conflict.IncomingCoverage == billing.EconomicEvidenceCoverageUnsupported && conflict.IncomingCoverageReason == wantReason {
			return true
		}
	}
	return false
}

func f4Terminalize(t *testing.T, session *attemptSession, intent attemptTerminalIntent, cmd sdkterminal.Command, outcome billing.LegOutcome) (billing.CallLegUsageRecord, error) {
	t.Helper()
	var captured *billing.CallLegUsageRecord
	session.observeBillingLeg = func(_ context.Context, record billing.CallLegUsageRecord) {
		cloned := record.Clone()
		captured = &cloned
	}
	result := session.TerminalizeAttempt(context.Background(), intent, attemptEvidence{Command: cmd, LegOutcome: outcome})
	if captured == nil {
		t.Fatal("direct terminal branch did not hand off a billing leg record")
	}
	return *captured, result.Result.Err
}

// TestF4TerminalHandoffRetainsFinalLocalMeasurementWhenQueueSaturated is the
// primary RED/GREEN regression. The prior local head (output tokens = 10) is
// durably checkpointed and flushed, the ordinary queue is saturated, and then
// the final local measurement (output tokens = 20) cannot be queued. The
// terminal record must carry the immutable final measurement, and the durable
// checkpoint truncation must stay visible as sticky capture loss that a later
// successful terminal flush does not clear.
func TestF4TerminalHandoffRetainsFinalLocalMeasurementWhenQueueSaturated(t *testing.T) {
	t.Parallel()
	sink := &refinement41ObservationSink{}
	session, boundary := f4LocalBoundaryAttempt(sink)

	// Step 1: valid local head output tokens = 10, durably checkpointed.
	f4ObserveProviderOutput(boundary, 40)
	session.checkpointLocalBoundaryObservations(session.economicCheckpointNow())
	if err := session.flushEconomicCheckpoints(context.Background(), true); err != nil {
		t.Fatalf("initial local checkpoint flush: %v", err)
	}
	const durableHeads = 3
	durable := sink.observationsSnapshot()
	if len(durable) != durableHeads {
		t.Fatalf("durable pre-terminal local observations=%d, want %d", len(durable), durableHeads)
	}
	if prior, ok := f4ObservationsLocalProviderOutputTokens(durable); !ok || prior != "10" {
		t.Fatalf("durable local prior head tokens=%q ok=%v, want 10", prior, ok)
	}

	// Step 2: saturate every ordinary pending+deferred slot.
	f4SaturateOrdinaryCheckpointQueue(t, session)

	// Step 3: the actual final local measurement is output tokens = 20.
	f4ObserveProviderOutput(boundary, 40)

	record, terminalErr := f4Terminalize(t, session, IntentSuccess, sdkterminal.CommandNormalFinish, billing.LegOutcomeWinner)
	if terminalErr != nil {
		t.Fatalf("terminal flush unexpectedly failed: %v", terminalErr)
	}

	if got := f4LocalProviderOutputTokens(t, record); got != "20" {
		t.Fatalf("terminal local provider-output tokens=%q, want final 20 (stale prior labeled as final)", got)
	}
	if !f4HasCheckpointCapacityLossMarker(record) {
		t.Fatalf("terminal record missing sticky checkpoint-capacity capture loss: %+v", record.EvidenceConflicts)
	}
	if loss := session.evidenceCaptureLossSnapshot(); !loss.present || !loss.causes.has(evidenceCaptureLossCheckpointCapacity) {
		t.Fatalf("capture loss not retained after successful terminal flush: %+v", loss)
	}
}

// TestF4TerminalHandoffRetainsMeasurementWhenTerminalFlushAlsoFails covers the
// continuing sink failure: the final local measurement must still reach the
// terminal record and the loss must still be recorded even though the terminal
// checkpoint flush returns an error.
func TestF4TerminalHandoffRetainsMeasurementWhenTerminalFlushAlsoFails(t *testing.T) {
	t.Parallel()
	sink := &refinement41ObservationSink{}
	session, boundary := f4LocalBoundaryAttempt(sink)

	f4ObserveProviderOutput(boundary, 40)
	session.checkpointLocalBoundaryObservations(session.economicCheckpointNow())
	if err := session.flushEconomicCheckpoints(context.Background(), true); err != nil {
		t.Fatalf("initial local checkpoint flush: %v", err)
	}

	f4SaturateOrdinaryCheckpointQueue(t, session)
	f4ObserveProviderOutput(boundary, 40)

	sink.mu.Lock()
	sink.err = errors.New("journal unavailable")
	sink.mu.Unlock()

	record, terminalErr := f4Terminalize(t, session, IntentSuccess, sdkterminal.CommandNormalFinish, billing.LegOutcomeWinner)
	if terminalErr == nil || !strings.Contains(terminalErr.Error(), "journal unavailable") {
		t.Fatalf("terminal flush error=%v, want journal unavailable", terminalErr)
	}
	if got := f4LocalProviderOutputTokens(t, record); got != "20" {
		t.Fatalf("terminal local provider-output tokens=%q, want final 20 despite flush failure", got)
	}
	if !f4HasCheckpointCapacityLossMarker(record) {
		t.Fatalf("terminal record missing sticky capture loss: %+v", record.EvidenceConflicts)
	}
}

// TestF4CancellationTerminalHandoffRetainsFinalLocalMeasurement covers the
// cancellation terminal intent through the same direct record branch.
func TestF4CancellationTerminalHandoffRetainsFinalLocalMeasurement(t *testing.T) {
	t.Parallel()
	sink := &refinement41ObservationSink{}
	session, boundary := f4LocalBoundaryAttempt(sink)

	f4ObserveProviderOutput(boundary, 40)
	session.checkpointLocalBoundaryObservations(session.economicCheckpointNow())
	if err := session.flushEconomicCheckpoints(context.Background(), true); err != nil {
		t.Fatalf("initial local checkpoint flush: %v", err)
	}

	f4SaturateOrdinaryCheckpointQueue(t, session)
	f4ObserveProviderOutput(boundary, 40)

	record, terminalErr := f4Terminalize(t, session, IntentCancellation, sdkterminal.CommandCancel, billing.LegOutcomeCanceled)
	if terminalErr != nil {
		t.Fatalf("cancellation terminal flush error: %v", terminalErr)
	}
	if got := f4LocalProviderOutputTokens(t, record); got != "20" {
		t.Fatalf("cancellation terminal local provider-output tokens=%q, want final 20", got)
	}
	if !f4HasCheckpointCapacityLossMarker(record) {
		t.Fatalf("cancellation terminal record missing sticky capture loss: %+v", record.EvidenceConflicts)
	}
}
