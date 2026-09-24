package runtime

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	aggregate "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// R5-C1 terminal-boundary closure: the R5-A/R5-B attempt-owned capture-loss and
// checkpoint-loss dispositions are only meaningful if they survive the real
// terminal construction/append seam and downstream retail selection. These
// tests drive the production terminal leg path (recordBillingLegForAttempt ->
// appendBillingLegStrict -> TerminalUsageSink) and the stock provider evidence
// buffer rather than sealing a synthetic record. The observation sink and the
// terminal harness are in-memory test doubles: this file makes no durable-write,
// restart, or recovery claim (that boundary is R5-C2). They fail if the sticky
// loss projection is dropped before the terminal leg or if a healthy
// >300-revision provider progression is misclassified as lossy.

const (
	r5cStoreID = "store-r5c"
	r5cALegID  = "a-r5c"
	r5cBLegID  = "b-r5c"
)

var r5cNow = time.Unix(1_700_200_000, 0).UTC()

// r5cProviderIdentity binds a provider evidence buffer to the B-leg scope that
// the terminal record will use, so drained observations pass the record and
// retail lineage checks.
func r5cProviderIdentity(callID billing.BillingCallID) coremetering.ObservationIdentity {
	return coremetering.ObservationIdentity{
		StoreID: r5cStoreID, RequestID: "req-r5c", CallID: callID.String(), BillingCallID: callID.String(),
		ALegID: r5cALegID, BLegID: r5cBLegID, AttemptID: "attempt-r5c", AttemptSeq: 1,
		ObservedAt: r5cNow, ReceivedAt: r5cNow,
	}
}

// r5cProviderDraft builds one native provider draft carrying a directional image
// media component and an aggregate provider charge. Delta semantics keep every
// revision a distinct economic identity, so a destructive single drain cannot
// coalesce the prefix.
func r5cProviderDraft(sourceKey string, revision uint64) coremetering.ProviderEvidenceDraft {
	media := metering.Decimal{Coefficient: strconv.FormatUint(revision, 10), Scale: 0}
	money := metering.Decimal{Coefficient: strconv.FormatUint(revision*100, 10), Scale: 0}
	return coremetering.ProviderEvidenceDraft{
		SourceEventKey: sourceKey, StreamID: "provider.r5c.v2",
		Semantics: metering.SemanticsDelta,
		Measures: []metering.Measure{{
			Key:   metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "r5c.provider.schema"},
			Value: &media, Quality: metering.QualityObserved,
		}},
		Charges: []metering.ReportedCharge{{
			ChargeItemID: fmt.Sprintf("charge:%s:%d", sourceKey, revision),
			Amount:       &money, Currency: "USD", Kind: metering.ChargeKindAggregate,
		}},
	}
}

// r5cProviderStream adapts the stock provider evidence buffer to the runtime's
// managed observation source so the terminal drain consumes the real buffer.
type r5cProviderStream struct {
	buffer *coremetering.ProviderEvidenceBuffer
}

func (*r5cProviderStream) Recv(context.Context) (lipapi.Event, error) { return lipapi.Event{}, nil }
func (*r5cProviderStream) Send(lipapi.Event) error                    { return nil }
func (*r5cProviderStream) Close() error                               { return nil }
func (*r5cProviderStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func (s *r5cProviderStream) DrainEconomicObservations() []metering.Observation {
	return s.buffer.DrainEconomicObservations()
}

// r5cCheckpointSink wraps the shared in-memory observation sink and counts each
// Append/AppendObservations attempt, including failed attempts. It lets the test
// prove that an unavailable-sink failure was actually executed instead of being
// assumed from a side flag the runtime never reaches.
type r5cCheckpointSink struct {
	refinement41ObservationSink
	appendCalls int
}

func (s *r5cCheckpointSink) Append(ctx context.Context, observation metering.Observation) error {
	s.appendCalls++
	return s.refinement41ObservationSink.Append(ctx, observation)
}

func (s *r5cCheckpointSink) AppendObservations(ctx context.Context, observations []metering.Observation) error {
	s.appendCalls++
	return s.refinement41ObservationSink.AppendObservations(ctx, observations)
}

func (s *r5cCheckpointSink) appendAttempts() int {
	return s.appendCalls
}

// r5cTerminalHarness is the real runtime terminal seam: the executor owns the
// terminal usage sink, the attempt owns the bounded checkpoint queue, and the
// leg is produced by recordBillingLegForAttempt.
type r5cTerminalHarness struct {
	stream *retryRecvStream
	legs   []billing.CallLegUsageRecord
}

func r5cNewTerminalHarness(t *testing.T, callID billing.BillingCallID) *r5cTerminalHarness {
	t.Helper()
	harness := &r5cTerminalHarness{}
	executor := &Executor{BillingRuntime: BillingRuntime{
		TerminalUsageSink: testTerminalSink{appendLeg: func(_ context.Context, record billing.CallLegUsageRecord) error {
			sealed, err := record.Seal()
			if err != nil {
				return err
			}
			harness.legs = append(harness.legs, sealed)
			return nil
		}},
		BillingIdentity: testBillingIdentity(),
	}}
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:        r5cALegID,
			billingCallID: callID,
			baseline:      lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-r5c"}},
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{BLegID: r5cBLegID, ALegID: r5cALegID, Seq: 1},
			routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
			authorityLifecycle{},
		),
	}
	stream = stampStreamIdentity(stream, executor)
	harness.stream = stream
	return harness
}

func (h *r5cTerminalHarness) attempt() *attemptSession {
	return h.stream.attempt.require()
}

// terminalize drives the one real terminal leg construction/append path: the
// production attempt terminalization callback, which builds the leg and hands
// it to appendIndependentCallLegStrict -> TerminalUsageSink.AppendLeg.
func (h *r5cTerminalHarness) terminalize(t *testing.T) {
	t.Helper()
	result := testTerminalizeAttempt(context.Background(), h.stream, sdkterminal.CommandNormalFinish, nil)
	if result.Err != nil {
		t.Fatalf("terminalize attempt: %v", result.Err)
	}
}

// TestR5CTerminalCaptureLossSurvivesAppendAndRetail is the primary R5-C1
// regression. A forced checkpoint flush exercises the unavailable in-memory
// observation sink: the append is attempted, fails, and the accepted prefix
// keeps its retry ownership. A destructive drain past the exhausted bound is
// then an irreversible capture loss, and later healthy (well-formed,
// provider-native) updates are still accepted after recovery. The terminal
// record appended to the terminal sink must carry the sticky capture-loss
// disposition so downstream retail selection cannot rate the known-truncated
// prefix COMPLETE.
func TestR5CTerminalCaptureLossSurvivesAppendAndRetail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	harness := r5cNewTerminalHarness(t, callID)
	attempt := harness.attempt()

	// Bind an unavailable checkpoint sink. Queue admission is independent of
	// sink availability, so the bounded queues still saturate; the failing
	// Append itself is exercised by the explicit forced flush below rather than
	// assumed from the sink error flag.
	unavailableErr := errors.New("journal unavailable")
	unavailable := &r5cCheckpointSink{}
	unavailable.err = unavailableErr
	attempt.observationSink = unavailable

	// Fill the bounded queues to capacity from the stock provider buffer.
	const feed = r5CheckpointCapacity + 32
	buffer := coremetering.NewProviderEvidenceBuffer()
	buffer.BindEconomicEvidence(r5cProviderIdentity(callID))
	for revision := uint64(1); revision <= r5CheckpointCapacity; revision++ {
		buffer.Add(r5cProviderDraft("provider.r5c.loss", revision))
	}
	attempt.drainStreamUsageEvidence(&r5cProviderStream{buffer: buffer})

	attempt.checkpointMu.Lock()
	admitted := len(attempt.checkpointPending) + len(attempt.checkpointDeferred)
	attempt.checkpointMu.Unlock()
	if admitted != r5CheckpointCapacity {
		t.Fatalf("admitted queue entries=%d, want %d", admitted, r5CheckpointCapacity)
	}

	// Execute a real forced flush while the sink is unavailable. The append must
	// be attempted and reported as a failure, and the whole accepted batch must
	// keep its retry ownership because the sink is atomic.
	if flushErr := attempt.flushEconomicCheckpoints(ctx, true); !errors.Is(flushErr, unavailableErr) {
		t.Fatalf("failing flush while sink unavailable error=%v, want %v", flushErr, unavailableErr)
	}
	if attempts := unavailable.appendAttempts(); attempts == 0 {
		t.Fatal("rejection setup never exercised the unavailable checkpoint sink Append")
	}
	attempt.checkpointMu.Lock()
	retained := len(attempt.checkpointPending) + len(attempt.checkpointDeferred)
	attempt.checkpointMu.Unlock()
	if retained != r5CheckpointCapacity {
		t.Fatalf("failed flush dropped retry intent: retained=%d, want %d", retained, r5CheckpointCapacity)
	}

	// A destructive drain of the distinct observations past the already
	// exhausted bound is now an irreversible capture loss.
	overflow := coremetering.NewProviderEvidenceBuffer()
	overflow.BindEconomicEvidence(r5cProviderIdentity(callID))
	for revision := uint64(r5CheckpointCapacity + 1); revision <= feed; revision++ {
		overflow.Add(r5cProviderDraft("provider.r5c.loss.overflow", revision))
	}
	attempt.drainStreamUsageEvidence(&r5cProviderStream{buffer: overflow})

	loss := attempt.evidenceCaptureLossSnapshot()
	if !loss.present || loss.count != feed-r5CheckpointCapacity {
		t.Fatalf("capture loss=%+v, want %d rejected observations", loss, feed-r5CheckpointCapacity)
	}
	if !loss.causes.has(evidenceCaptureLossCheckpointCapacity) {
		t.Fatalf("capture loss causes=%08b, want checkpoint-capacity bit", loss.causes)
	}

	// Both retry queues stay exhausted, so the accepted prefix keeps its bounded
	// queue ownership and the rejected suffix is the irreversible loss.
	attempt.checkpointMu.Lock()
	exhausted := len(attempt.checkpointPending) + len(attempt.checkpointDeferred)
	attempt.checkpointMu.Unlock()
	if exhausted != r5CheckpointCapacity {
		t.Fatalf("accepted queue entries=%d, want exhausted capacity %d", exhausted, r5CheckpointCapacity)
	}

	// Later healthy updates: heal the sink, flush the queue, and accept a final
	// provider-native media + money observation. The sticky loss must survive.
	unavailable.err = nil
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("recovery flush: %v", err)
	}
	// The accepted prefix was handed to the (in-memory) observation sink and the
	// checkpoint queue drained; recoverable accounting intent is preserved even
	// though the suffix was lost. This is not a restart or durability claim.
	if written := len(unavailable.observationsSnapshot()); written != r5CheckpointCapacity {
		t.Fatalf("sink recoverable prefix=%d, want %d", written, r5CheckpointCapacity)
	}
	finalBuffer := coremetering.NewProviderEvidenceBuffer()
	finalBuffer.BindEconomicEvidence(r5cProviderIdentity(callID))
	finalBuffer.Add(r5cProviderDraft("provider.r5c.loss.final", uint64(feed+1)))
	final := finalBuffer.DrainEconomicObservations()
	if len(final) != 1 {
		t.Fatalf("final healthy observation drain=%d, want 1", len(final))
	}
	attempt.rememberEconomicObservationOnce(final[0])
	if recovered := attempt.evidenceCaptureLossSnapshot(); !recovered.present || recovered.count != loss.count {
		t.Fatalf("later healthy update changed sticky capture loss: %+v", recovered)
	}

	// Real terminal construction/append.
	harness.terminalize(t)

	if len(harness.legs) != 1 {
		t.Fatalf("terminal appended legs=%d, want 1", len(harness.legs))
	}
	leg := harness.legs[0]
	if len(leg.EvidenceConflicts) == 0 {
		t.Fatal("terminal record sealed a known-truncated prefix without any evidence conflict")
	}
	wantReason := evidenceCaptureLossConflictReasonPrefix + evidenceCaptureLossCheckpointCapacity.reason()
	markerVisible := false
	for _, conflict := range leg.EvidenceConflicts {
		if conflict.IncomingCoverage == billing.EconomicEvidenceCoverageUnsupported &&
			conflict.IncomingCoverageReason == wantReason {
			markerVisible = true
		}
	}
	if !markerVisible {
		t.Fatalf("appended leg lost the reserved capture-loss marker: %+v", leg.EvidenceConflicts)
	}

	// Downstream retail selection must not treat the leg as trusted.
	if _, err := r5cSelectRetail(leg); !errors.Is(err, billing.ErrRetailSelectionUntrusted) {
		t.Fatalf("retail selection on truncated leg error=%v, want %v", err, billing.ErrRetailSelectionUntrusted)
	}
	if _, err := r5cRateSelectedRetail(leg); !errors.Is(err, billing.ErrRetailSelectionUntrusted) {
		t.Fatalf("rate-selected retail on truncated leg error=%v, want %v", err, billing.ErrRetailSelectionUntrusted)
	}
}

// TestR5CTerminalHealthyProviderProgressionRetainsFinalNativeValues proves the
// same real terminal seam does not manufacture capture loss for a healthy
// >300-revision provider progression: the appended leg retains every native
// delta observation with valid references, the production reducer sums them to
// the exact provider media and money total, and retail selection still
// completes.
func TestR5CTerminalHealthyProviderProgressionRetainsFinalNativeValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	harness := r5cNewTerminalHarness(t, callID)
	attempt := harness.attempt()
	healthy := &refinement41ObservationSink{}
	attempt.observationSink = healthy

	// The stock provider buffer is drained and flushed in bounded batches, the
	// real receive-path cadence, so the bounded queue never saturates.
	const revisions = 320
	buffer := coremetering.NewProviderEvidenceBuffer()
	buffer.BindEconomicEvidence(r5cProviderIdentity(callID))
	source := &r5cProviderStream{buffer: buffer}
	for revision := uint64(1); revision <= revisions; revision++ {
		buffer.Add(r5cProviderDraft("provider.r5c.healthy", revision))
		drained := source.DrainEconomicObservations()
		if len(drained) != 1 {
			t.Fatalf("revision %d: drained=%d, want 1", revision, len(drained))
		}
		attempt.rememberEconomicObservationOnce(drained[0])
		if revision%preTerminalEconomicCheckpointBatch == 0 || revision == revisions {
			if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
				t.Fatalf("revision %d: flush: %v", revision, err)
			}
		}
	}

	if loss := attempt.evidenceCaptureLossSnapshot(); loss.present {
		t.Fatalf("healthy provider progression manufactured capture loss: %+v", loss)
	}

	harness.terminalize(t)

	if len(harness.legs) != 1 {
		t.Fatalf("terminal appended legs=%d, want 1", len(harness.legs))
	}
	leg := harness.legs[0]
	if len(leg.EvidenceConflicts) != 0 {
		t.Fatalf("healthy progression appended conflicts=%+v, want none", leg.EvidenceConflicts)
	}
	if len(leg.Observations) != revisions {
		t.Fatalf("appended leg observations=%d, want %d", len(leg.Observations), revisions)
	}
	for i, observation := range leg.Observations {
		if err := observation.Validate(); err != nil {
			t.Fatalf("appended observation %d invalid: %v", i, err)
		}
		if _, err := observation.Ref(r5cStoreID); err != nil {
			t.Fatalf("appended observation %d ref invalid: %v", i, err)
		}
	}
	quantity, money := r5cReducedCommercialState(t, leg.Observations, r5cMediaKey())
	if quantity != "51360" {
		t.Fatalf("terminal reduced provider media=%q, want 51360", quantity)
	}
	if money != "5136000" {
		t.Fatalf("terminal reduced provider money=%q, want 5136000", money)
	}

	selection, err := r5cSelectRetail(leg)
	if err != nil {
		t.Fatalf("healthy progression broke retail selection: %v", err)
	}
	if len(selection.SelectedBLegs) != 1 || len(selection.ObservationRefs) != revisions {
		t.Fatalf("retail selection=%+v, want one leg with %d refs", selection, revisions)
	}
}

func r5cMediaKey() metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionInput, Component: metering.ComponentImage,
		Unit: metering.UnitImage, SchemaID: "r5c.provider.schema",
	}
}

// r5cReducedCommercialState reduces the terminal leg observations with the
// production V2 aggregate reducer and returns the exact total provider media
// quantity and provider money. Delta provider evidence carries a distinct
// source/charge scope per revision, so the effective reduced scopes are disjoint
// economic units and the commercial total is their sum -- never a hand-picked
// latest revision.
func r5cReducedCommercialState(t *testing.T, observations []metering.Observation, key metering.ComponentKey) (quantity, money string) {
	t.Helper()
	snapshot, err := aggregate.ApplyObservations(observations)
	if err != nil {
		t.Fatalf("production aggregate reduce: %v", err)
	}
	normalized, err := key.Normalize()
	if err != nil {
		t.Fatalf("normalize component key: %v", err)
	}
	quantityRat := new(big.Rat)
	moneyRat := new(big.Rat)
	for _, measure := range snapshot.Measures {
		if !measure.Complete || !measure.Key.Equal(normalized) {
			continue
		}
		value, valueErr := measure.Value.ToRat()
		if valueErr != nil {
			t.Fatalf("reduced media value: %v", valueErr)
		}
		quantityRat.Add(quantityRat, value)
	}
	for _, charge := range snapshot.Charges {
		if !charge.Complete || charge.Charge.Amount == nil {
			continue
		}
		value, valueErr := charge.Charge.Amount.ToRat()
		if valueErr != nil {
			t.Fatalf("reduced provider money: %v", valueErr)
		}
		moneyRat.Add(moneyRat, value)
	}
	return quantityRat.RatString(), moneyRat.RatString()
}

func r5cSelectRetail(leg billing.CallLegUsageRecord) (billing.RetailSelectionResult, error) {
	call, policy := r5cRetailScope(leg)
	return billing.SelectRetailBLegEvidence(billing.RetailSelectionInput{
		Call: call, Legs: []billing.CallLegUsageRecord{leg}, Policy: policy,
	})
}

func r5cRateSelectedRetail(leg billing.CallLegUsageRecord) (billing.RetailRatingResult, error) {
	call, policy := r5cRetailScope(leg)
	return billing.RateSelectedRetailBLegs(context.Background(), billing.RetailRatingInput{
		Call: call, Legs: []billing.CallLegUsageRecord{leg}, Policy: policy,
	})
}

func r5cRetailScope(leg billing.CallLegUsageRecord) (billing.CallUsageRecord, billing.ChargePolicy) {
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion,
		CallID:        leg.CallID, SubmissionID: leg.SubmissionID, AccountID: "acct-r5c",
		ALegID: r5cALegID, SessionID: "sess-r5c",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing-r5c", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy-r5c", Version: "v1"},
		ExpectedBLegIDs:    []string{leg.BLegID},
	}
	policy := billing.ChargePolicy{
		Ref:                 call.ChargePolicyRef,
		PricingRef:          call.CustomerPricingRef,
		Scope:               billing.ChargeAllPotentialLegs,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
	}
	return call, policy
}
