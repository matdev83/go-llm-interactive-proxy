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
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
	_ "modernc.org/sqlite"
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
// a unique, non-coalescing accepted delta observation scoped to the attempt's
// durable store/call identity. The identity scoping is required by any REAL
// observation journal (and by the terminal leg validator): an unscoped
// observation would be rejected as out-of-scope rather than saturating the
// queue. Distinct revisions keep every delta observation a distinct economic
// identity, so no two coalesce.
func f4SaturateOrdinaryCheckpointQueue(t *testing.T, session *attemptSession) {
	t.Helper()
	total := maxPreTerminalEconomicCheckpointPending + maxPreTerminalEconomicCheckpointDeferred
	for i := uint64(1); i <= uint64(total); i++ {
		observation := f4OrdinaryCheckpointObservation(session, i)
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

// f4OrdinaryCheckpointObservation builds one distinct non-coalescing ordinary
// delta observation in the attempt's own durable scope.
func f4OrdinaryCheckpointObservation(session *attemptSession, revision uint64) metering.Observation {
	observation := refinement41Observation(revision, "1")
	observation.Semantics = metering.SemanticsDelta
	observation.ID = fmt.Sprintf("f4-ordinary-%d", revision)
	observation.SourceEventKey = "f4-ordinary-checkpoint"
	observation.StreamID = "f4-ordinary-stream"
	storeID := session.billingStoreID
	callID := session.billingCallID.String()
	observation.Subject = metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: storeID,
		CallID: callID, BillingCallID: callID,
		ALegID: session.bleg.ALegID, BLegID: session.bleg.BLegID,
		AttemptID: session.bleg.BLegID, AttemptSeq: uint64(session.bleg.Seq),
	}
	observation.Correlation = metering.CorrelationV2{
		StoreID: storeID, CallID: callID, BillingCallID: callID,
		ALegID: session.bleg.ALegID, BLegID: session.bleg.BLegID,
		AttemptID: session.bleg.BLegID, AttemptSeq: uint64(session.bleg.Seq),
	}
	return observation
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

// f4IsCheckpointCapacityLossConflict reports whether one conflict is the
// runtime-owned reserved capture-loss marker for checkpoint-capacity loss.
func f4IsCheckpointCapacityLossConflict(conflict billing.EvidenceConflict) bool {
	wantReason := evidenceCaptureLossConflictReasonPrefix + evidenceCaptureLossCheckpointCapacity.reason()
	return conflict.IncomingCoverage == billing.EconomicEvidenceCoverageUnsupported && conflict.IncomingCoverageReason == wantReason
}

// f4HasCheckpointCapacityLossMarker reports whether the record carries the
// runtime-owned reserved capture-loss marker for checkpoint-capacity loss.
func f4HasCheckpointCapacityLossMarker(record billing.CallLegUsageRecord) bool {
	for _, conflict := range record.EvidenceConflicts {
		if f4IsCheckpointCapacityLossConflict(conflict) {
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

// f4FileBillingStore owns one file-backed durable billing-store connection so a
// test can close and reopen the same database file to model a process restart.
// Unlike the in-memory observation-sink doubles, this uses the production
// billingstore constructor, so the terminal leg is proven durable, not merely
// handed to a callback.
type f4FileBillingStore struct {
	store *billingstore.DurableStore
	sqlDB *sql.DB
}

func f4OpenFileBillingStore(t *testing.T, path, storeID string) *f4FileBillingStore {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("f4 open billing sqlite: %v", err)
	}
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatalf("f4 billing bun db: %v", err)
	}
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: storeID})
	if err != nil {
		_ = sqlDB.Close()
		t.Fatalf("f4 durable billing store: %v", err)
	}
	return &f4FileBillingStore{store: store, sqlDB: sqlDB}
}

func (s *f4FileBillingStore) close(t *testing.T) {
	t.Helper()
	if s == nil {
		return
	}
	if s.store != nil {
		if err := s.store.Close(); err != nil {
			t.Fatalf("f4 close billing store: %v", err)
		}
	}
	if s.sqlDB != nil {
		if err := s.sqlDB.Close(); err != nil {
			t.Fatalf("f4 close billing sql: %v", err)
		}
	}
}

// f4SelectRetail selects the durable sealed leg with the call lineage aligned
// to the leg, so selection is exercised against the leg's own evidence rather
// than failing on a synthetic call/leg lineage mismatch.
func f4SelectRetail(leg billing.CallLegUsageRecord) (billing.RetailSelectionResult, error) {
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion,
		CallID:        leg.CallID, SubmissionID: leg.SubmissionID, AccountID: "acct-f4",
		ALegID: leg.ALegID, SessionID: "sess-f4",
		StartedAt: leg.StartedAt, FinishedAt: leg.FinishedAt,
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing-f4", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy-f4", Version: "v1"},
		ExpectedBLegIDs:    []string{leg.BLegID},
	}
	policy := billing.ChargePolicy{
		Ref:                 call.ChargePolicyRef,
		PricingRef:          call.CustomerPricingRef,
		Scope:               billing.ChargeAllPotentialLegs,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
	}
	return billing.SelectRetailBLegEvidence(billing.RetailSelectionInput{
		Call: call, Legs: []billing.CallLegUsageRecord{leg}, Policy: policy,
	})
}

// f4RetailScopedObservation reports whether one observation is valid
// backend-attempt retail evidence. The retail selector accepts only BLeg
// backend-attempt observations on the backend ingress/egress boundaries; the
// local frontend-egress customer plane is retained on the durable record but is
// outside retail's ratable scope (and is rejected before the evidence-conflict
// gate).
func f4RetailScopedObservation(observation metering.Observation) bool {
	if observation.Subject.Kind != metering.SubjectBLeg || observation.Lifecycle != metering.LifecycleBackendAttempt {
		return false
	}
	return observation.Boundary == metering.BoundaryBackendIngress || observation.Boundary == metering.BoundaryBackendEgress
}

// f4ScopedRetailLeg clones the reopened immutable leg and neutralizes only
// observations outside valid backend-attempt retail scope. The accepted
// ordinary provider evidence and every EvidenceConflict are preserved, and the
// durable original is never mutated.
func f4ScopedRetailLeg(leg billing.CallLegUsageRecord) billing.CallLegUsageRecord {
	scoped := leg.Clone()
	scoped.Observations = slices.DeleteFunc(scoped.Observations, func(observation metering.Observation) bool {
		return !f4RetailScopedObservation(observation)
	})
	return scoped
}

// f4ClearCheckpointCapacityLoss clones the leg and clears only the
// runtime-owned checkpoint-capacity capture-loss conflict, preserving every
// other conflict so unrelated failures cannot be masked.
func f4ClearCheckpointCapacityLoss(leg billing.CallLegUsageRecord) billing.CallLegUsageRecord {
	control := leg.Clone()
	control.EvidenceConflicts = slices.DeleteFunc(control.EvidenceConflicts, f4IsCheckpointCapacityLossConflict)
	return control
}

// f4AssertLossMarkerBlocksCompleteRetail proves the checkpoint-capacity
// capture-loss marker is what blocks complete customer valuation. The
// scope-clean clone (frontend-egress observations removed, all evidence
// conflicts retained) must still be refused with the exact untrusted-evidence
// classification, and clearing only that one loss conflict from the same clone
// must let the otherwise-valid evidence select a complete retail result.
func f4AssertLossMarkerBlocksCompleteRetail(t *testing.T, durable billing.CallLegUsageRecord) {
	t.Helper()
	scoped := f4ScopedRetailLeg(durable)
	if _, err := f4SelectRetail(scoped); !errors.Is(err, billing.ErrRetailSelectionUntrusted) {
		t.Fatalf("scope-clean durable leg with checkpoint-capacity capture loss selection error=%v, want %v", err, billing.ErrRetailSelectionUntrusted)
	}
	control := f4ClearCheckpointCapacityLoss(scoped)
	result, err := f4SelectRetail(control)
	if err != nil {
		t.Fatalf("control durable leg with only the capture-loss conflict cleared selection error=%v, want complete", err)
	}
	if result.Completeness != billing.RetailSelectionComplete {
		t.Fatalf("control durable leg selection completeness=%q, want %q", result.Completeness, billing.RetailSelectionComplete)
	}
}

// TestF4TerminalHandoffRetainsFinalLocalMeasurementAcrossDurableRestart is the
// durable counterpart to the in-memory handoff regressions. It drives the same
// real direct attemptSession.TerminalizeAttempt branch (no BillingLegFn) with a
// REAL file-backed observation journal and a REAL durable billing store, then
// closes and reopens both files and reads the sealed leg back.
//
// The prior local head (output tokens = 10) is durably checkpointed, all 128
// ordinary checkpoint slots are saturated, and the final local measurement
// (output tokens = 20) cannot be queued. The durable sealed leg must carry the
// immutable final 20 (never the stale 10 republished as final) with the sticky
// checkpoint-capacity capture loss intact, and the observation journal must
// still show only the accepted 10 prefix: the final 20 has no durable
// checkpoint owner, so the loss is genuine rather than a stale republish.
func TestF4TerminalHandoffRetainsFinalLocalMeasurementAcrossDurableRestart(t *testing.T) {
	// This durable proof drives the real 2-second terminal cleanup budget through
	// a full 128-slot checkpoint flush on file-backed SQLite. Running it in the
	// package parallel pool lets an unrelated heavy durable test share the core
	// during that budget, so the batch append can consume the whole terminal
	// deadline before the billing leg append begins. Keep it sequential: the
	// assertion proof is unchanged, only the avoidable package-level contention
	// is removed.
	ctx := context.Background()

	const storeID = "store-f4"
	dir := t.TempDir()
	journalPath := filepath.Join(dir, "f4-journal.sqlite")
	billingPath := filepath.Join(dir, "f4-billing.sqlite")

	journal := r5c2bOpenFileStore(t, journalPath, storeID)
	billingFile := f4OpenFileBillingStore(t, billingPath, storeID)

	session, boundary := f4LocalBoundaryAttempt(journalstore.NewObservationSink(journal.store))
	session.submissionID = "submission-f4"
	session.appendBillingLegStrict = func(appendCtx context.Context, _ billing.BillingCallID, record billing.CallLegUsageRecord) error {
		return billingFile.store.AppendCallLegUsage(appendCtx, record)
	}

	// Step 1: valid local head output tokens = 10, durably checkpointed.
	f4ObserveProviderOutput(boundary, 40)
	session.checkpointLocalBoundaryObservations(session.economicCheckpointNow())
	if err := session.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("initial durable local checkpoint flush: %v", err)
	}

	// Step 2: saturate every ordinary pending+deferred slot.
	f4SaturateOrdinaryCheckpointQueue(t, session)

	// Step 3: the actual final local measurement is output tokens = 20.
	f4ObserveProviderOutput(boundary, 40)

	record, terminalErr := f4Terminalize(t, session, IntentSuccess, sdkterminal.CommandNormalFinish, billing.LegOutcomeWinner)
	if terminalErr != nil {
		t.Fatalf("terminal drain/flush unexpectedly failed: %v", terminalErr)
	}

	// Reopen both durable stores from their files to model a process restart.
	journal.close(t)
	billingFile.close(t)
	restartedJournal := r5c2bOpenFileStore(t, journalPath, storeID)
	defer restartedJournal.close(t)
	restartedBilling := f4OpenFileBillingStore(t, billingPath, storeID)
	defer restartedBilling.close(t)

	legKey, err := billing.CallLegUsageKey(record.CallID, record.BLegID)
	if err != nil {
		t.Fatalf("sealed call-leg key: %v", err)
	}
	durable, err := restartedBilling.store.GetCallLegUsage(ctx, legKey)
	if err != nil {
		t.Fatalf("durable sealed leg after restart: %v", err)
	}
	if got := f4LocalProviderOutputTokens(t, durable); got != "20" {
		t.Fatalf("durable sealed leg local provider-output tokens=%q, want final 20 (stale prior would be 10)", got)
	}
	if !f4HasCheckpointCapacityLossMarker(durable) {
		t.Fatalf("durable sealed leg lost the checkpoint-capacity capture loss disposition: %+v", durable.EvidenceConflicts)
	}
	// No false complete result: the checkpoint-capacity capture-loss marker must
	// be the disposition that blocks complete customer valuation. The reopened
	// immutable leg is never mutated; the proof runs on a scope-clean clone.
	f4AssertLossMarkerBlocksCompleteRetail(t, durable)

	// The durable observation journal holds exactly the accepted checkpoint
	// prefix: the local 10 is durable while the final local 20 never received a
	// durable checkpoint owner.
	const ordinaryCheckpoints = maxPreTerminalEconomicCheckpointPending + maxPreTerminalEconomicCheckpointDeferred
	const localPrefixObservations = 3
	page, err := restartedJournal.store.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: storeID, SubjectKind: metering.SubjectBLeg, SubjectID: record.BLegID, Limit: 500,
	})
	if err != nil {
		t.Fatalf("list durable observations after restart: %v", err)
	}
	if want := localPrefixObservations + ordinaryCheckpoints; len(page.Observations) != want {
		t.Fatalf("durable observations=%d, want local prefix %d + ordinary %d", len(page.Observations), localPrefixObservations, ordinaryCheckpoints)
	}
	durablePrior := false
	for _, observation := range page.Observations {
		if observation.Origin != metering.OriginLocal || observation.Boundary != metering.BoundaryBackendIngress {
			continue
		}
		for _, measure := range observation.Measures {
			if measure.Key.Direction != metering.DirectionOutput || measure.Key.Component != metering.ComponentTextToken || measure.Value == nil {
				continue
			}
			switch measure.Value.Coefficient {
			case "10":
				durablePrior = true
			case "20":
				t.Fatalf("final local 20 unexpectedly has a durable checkpoint owner: %+v", observation)
			}
		}
	}
	if !durablePrior {
		t.Fatal("durable journal lost the pre-terminal local provider-output 10 prefix observation")
	}
}

// TestF4TerminalHandoffRetainsFinalLocalMeasurementWhenDurableSinkFails is the
// continuing sink-failure durable variant: the file-backed observation journal
// becomes unavailable before terminalization, so the terminal checkpoint flush
// fails and neither the saturated ordinary queue nor the rejected final local
// measurement can become durable. The final local 20 and the sticky
// capture-loss disposition must still reach the independent durable billing
// store, so a restart still observes the immutable final measurement and the
// fail-closed disposition despite the checkpoint outage.
func TestF4TerminalHandoffRetainsFinalLocalMeasurementWhenDurableSinkFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const storeID = "store-f4"
	dir := t.TempDir()
	journalPath := filepath.Join(dir, "f4-journal-failing.sqlite")
	billingPath := filepath.Join(dir, "f4-billing-failing.sqlite")

	journal := r5c2bOpenFileStore(t, journalPath, storeID)
	billingFile := f4OpenFileBillingStore(t, billingPath, storeID)

	realSink, ok := journalstore.NewObservationSink(journal.store).(metering.AtomicObservationSink)
	if !ok {
		t.Fatal("file-backed observation sink is not atomic")
	}
	sink := &r5c2bFlakySink{delegate: realSink, available: true}
	session, boundary := f4LocalBoundaryAttempt(sink)
	session.submissionID = "submission-f4"
	session.appendBillingLegStrict = func(appendCtx context.Context, _ billing.BillingCallID, record billing.CallLegUsageRecord) error {
		return billingFile.store.AppendCallLegUsage(appendCtx, record)
	}

	f4ObserveProviderOutput(boundary, 40)
	session.checkpointLocalBoundaryObservations(session.economicCheckpointNow())
	if err := session.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("initial durable local checkpoint flush: %v", err)
	}

	f4SaturateOrdinaryCheckpointQueue(t, session)
	f4ObserveProviderOutput(boundary, 40)

	// The journal is unavailable for the terminal flush: neither the saturated
	// ordinary queue nor the rejected final local measurement is durable.
	sink.setAvailable(false)

	record, terminalErr := f4Terminalize(t, session, IntentSuccess, sdkterminal.CommandNormalFinish, billing.LegOutcomeWinner)
	if terminalErr == nil || !strings.Contains(terminalErr.Error(), "r5c2b journal unavailable") {
		t.Fatalf("terminal flush error=%v, want r5c2b journal unavailable", terminalErr)
	}

	journal.close(t)
	billingFile.close(t)
	restartedJournal := r5c2bOpenFileStore(t, journalPath, storeID)
	defer restartedJournal.close(t)
	restartedBilling := f4OpenFileBillingStore(t, billingPath, storeID)
	defer restartedBilling.close(t)

	legKey, err := billing.CallLegUsageKey(record.CallID, record.BLegID)
	if err != nil {
		t.Fatalf("sealed call-leg key: %v", err)
	}
	durable, err := restartedBilling.store.GetCallLegUsage(ctx, legKey)
	if err != nil {
		t.Fatalf("durable sealed leg after restart: %v", err)
	}
	if got := f4LocalProviderOutputTokens(t, durable); got != "20" {
		t.Fatalf("durable sealed leg local provider-output tokens=%q, want final 20 despite sink failure", got)
	}
	if !f4HasCheckpointCapacityLossMarker(durable) {
		t.Fatalf("durable sealed leg lost the checkpoint-capacity capture loss disposition: %+v", durable.EvidenceConflicts)
	}
	// Same durable-selection proof as the successful-flush variant: the reopened
	// immutable leg is never mutated, and the checkpoint-capacity loss marker
	// alone must be what blocks a complete retail result.
	f4AssertLossMarkerBlocksCompleteRetail(t, durable)

	// The unavailable journal holds only the initial local prefix: none of the
	// saturated ordinary observations nor the final local 20 could be persisted.
	page, err := restartedJournal.store.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: storeID, SubjectKind: metering.SubjectBLeg, SubjectID: record.BLegID, Limit: 500,
	})
	if err != nil {
		t.Fatalf("list durable observations after sink failure: %v", err)
	}
	if len(page.Observations) != 3 {
		t.Fatalf("durable observations=%d, want only the 3-observation local prefix", len(page.Observations))
	}
}
