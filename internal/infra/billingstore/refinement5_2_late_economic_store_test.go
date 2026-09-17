package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	_ "modernc.org/sqlite"
)

// TestRefinement52DurableLateEvidenceKeepsClosedLegAndRevisionWorkAppendable
// uses the production terminal sink to seal one positive-sequence B-leg, then
// reopens only the metering journal before appending provider finalizer,
// statement, and correction evidence. The billing leg row is never rewritten.
func TestRefinement52DurableLateEvidenceKeepsClosedLegAndRevisionWorkAppendable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeID := fmt.Sprintf("refinement52-%d", time.Now().UnixNano())
	billingStore := openRefinement52BillingStore(t, storeID)
	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	leg := refinement52ClosedLeg(storeID, callID)
	if err := billingStore.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatalf("terminal leg append: %v", err)
	}
	legKey, err := corebilling.CallLegUsageKey(callID, leg.BLegID)
	if err != nil {
		t.Fatal(err)
	}
	sealedBefore, err := billingStore.GetCallLegUsage(ctx, legKey)
	if err != nil {
		t.Fatalf("read sealed leg: %v", err)
	}
	legRowsBefore, err := billingStore.ListCallLegUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legRowsBefore) != 1 {
		t.Fatalf("closed leg rows before late evidence = %d, want 1", len(legRowsBefore))
	}

	journalPath := filepath.Join(t.TempDir(), "metering.sqlite")
	journal := openRefinement52Journal(t, journalPath, storeID)
	appender, err := coremetering.NewLateEconomicAppender(coremetering.LateEconomicAppenderConfig{
		StoreID: storeID, Legs: billingStore,
		Sink: journalstore.NewObservationSinkWithOutbox(journal), Resolver: journal,
	})
	if err != nil {
		t.Fatalf("late appender: %v", err)
	}

	finalizer := refinement52ProviderObservation(storeID, callID, 1, "finalizer", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	if err := appender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{
		Kind:     coremetering.LateEconomicProviderFinalizer,
		Identity: refinement52LateIdentity(storeID, callID), Observation: finalizer,
	}); err != nil {
		t.Fatalf("late provider finalizer append: %v", err)
	}
	statement := refinement52StatementObservation(storeID, callID, 1)
	if err := appender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{
		Kind:     coremetering.LateEconomicStatement,
		Identity: refinement52LateIdentity(storeID, callID), Observation: statement,
	}); err != nil {
		t.Fatalf("late statement append: %v", err)
	}

	pending, err := journal.ListPendingObservationOutbox(ctx, 10)
	if err != nil {
		t.Fatalf("list pending late evidence: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending late evidence = %d, want 2", len(pending))
	}

	// Simultaneous exact replay is handled by the atomic production sink. Every
	// caller succeeds, but the durable journal/outbox retains one identity.
	const duplicateWriters = 8
	var wg sync.WaitGroup
	errs := make(chan error, duplicateWriters)
	for i := 0; i < duplicateWriters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- appender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{
				Kind:     coremetering.LateEconomicProviderFinalizer,
				Identity: refinement52LateIdentity(storeID, callID), Observation: finalizer,
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent exact replay: %v", err)
		}
	}
	pending, err = journal.ListPendingObservationOutbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending after concurrent replay = %d, want 2", len(pending))
	}

	// Close/reopen the metering worker boundary. The same closed leg identity
	// remains the only reader key and accepts a correction against the durable
	// finalizer predecessor.
	if err := journal.Close(); err != nil {
		t.Fatalf("close metering journal: %v", err)
	}
	journal = openRefinement52Journal(t, journalPath, storeID)
	appender, err = coremetering.NewLateEconomicAppender(coremetering.LateEconomicAppenderConfig{
		StoreID: storeID, Legs: billingStore,
		Sink: journalstore.NewObservationSinkWithOutbox(journal), Resolver: journal,
	})
	if err != nil {
		t.Fatalf("restarted late appender: %v", err)
	}
	correction := refinement52ProviderObservation(storeID, callID, 2, "correction", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	correction.SourceEventKey = finalizer.SourceEventKey
	correction.Subject = finalizer.Subject
	correction.Correlation = finalizer.Correlation
	correction.Semantics = metering.SemanticsCorrection
	correction.Charges[0].ChargeItemID = finalizer.Charges[0].ChargeItemID
	correctionAmount := metering.DecimalFromNanoUnits(9)
	correction.Charges[0].Amount = &correctionAmount
	priorRef, err := finalizer.Ref(storeID)
	if err != nil {
		t.Fatalf("finalizer ref: %v", err)
	}
	correction.Supersedes = []metering.ObservationRef{priorRef}
	if err := appender.AppendLateEconomicEvidence(ctx, coremetering.LateEconomicEvidence{
		Kind:     coremetering.LateEconomicCorrection,
		Identity: refinement52LateIdentity(storeID, callID), Observation: correction,
	}); err != nil {
		t.Fatalf("late correction append after restart: %v", err)
	}

	page, err := journal.ListObservations(ctx, journalstore.ObservationQuery{StoreID: storeID, StreamID: "refinement52-stream", Limit: 10})
	if err != nil {
		t.Fatalf("list durable observations: %v", err)
	}
	if len(page.Observations) != 3 {
		t.Fatalf("durable observation revisions = %d, want 3", len(page.Observations))
	}
	if len(page.Observations[2].Supersedes) != 1 || !page.Observations[2].Supersedes[0].Equal(priorRef) {
		t.Fatalf("correction supersession = %+v, want exact finalizer ref %+v", page.Observations[2].Supersedes, priorRef)
	}
	if _, err := journal.GetObservation(ctx, correction.ID, correction.Revision); err != nil {
		t.Fatalf("restarted correction read: %v", err)
	}

	sealedAfter, err := billingStore.GetCallLegUsage(ctx, sealedBefore.Key)
	if err != nil {
		t.Fatalf("read leg after late evidence: %v", err)
	}
	if sealedAfter.Fingerprint != sealedBefore.Fingerprint || sealedAfter.AttemptSeq != sealedBefore.AttemptSeq || sealedAfter.BLegID != sealedBefore.BLegID {
		t.Fatalf("closed leg changed after late evidence: before=%+v after=%+v", sealedBefore, sealedAfter)
	}
	legRowsAfter, err := billingStore.ListCallLegUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legRowsAfter) != len(legRowsBefore) {
		t.Fatalf("closed leg rows after late evidence = %d, want %d", len(legRowsAfter), len(legRowsBefore))
	}
	providerSnapshot, err := aggregate.ApplyObservations([]metering.Observation{finalizer, correction})
	if err != nil {
		t.Fatalf("reduce provider revisions: %v", err)
	}
	if !providerSnapshot.Complete || !providerSnapshot.Payable || len(providerSnapshot.Charges) != 1 || providerSnapshot.Charges[0].Charge.Amount == nil {
		t.Fatalf("provider correction reduction = %+v, want one payable effective charge of 9", providerSnapshot)
	}
	providerAmount, err := providerSnapshot.Charges[0].Charge.Amount.ToNanoUnits()
	if err != nil || providerAmount != 9 {
		t.Fatalf("provider correction amount = %d (%v), want 9 ledger nanos", providerAmount, err)
	}

	// The existing pure bridge sees provider finalizer, statement, and
	// correction as one B-leg provider plane. No customer work is manufactured
	// from these operator-authoritative late charges.
	builder, err := corebilling.NewObservationEconomicWorkBuilder(corebilling.ObservationEconomicWorkBuilderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	works, err := builder.BuildEconomicRevisionWork(ctx, page.Observations)
	if err != nil {
		t.Fatalf("build revision work: %v", err)
	}
	if len(works) != 1 || works[0].Queue != corebilling.EconomicQueueProvider || works[0].EvidenceRevision != 2 {
		t.Fatalf("late economic work = %+v, want one provider revision 2", works)
	}
}

func TestRefinement52DurableSameRevisionEvidenceContainmentFencesHead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeID := fmt.Sprintf("refinement52-containment-%d", time.Now().UnixNano())
	store := openRefinement52BillingStore(t, storeID)
	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := corebilling.NewObservationEconomicWorkBuilder(corebilling.ObservationEconomicWorkBuilderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	a := refinement52ProviderObservation(storeID, callID, 1, "containment-a", metering.AcquisitionProviderResponse, metering.OriginProvider, metering.AuthorityObservedClaim)
	b := refinement52ProviderObservation(storeID, callID, 3, "containment-b", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	c := refinement52ProviderObservation(storeID, callID, 2, "containment-c", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	d := refinement52ProviderObservation(storeID, callID, 2, "containment-d", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	workFor := func(observations ...metering.Observation) corebilling.EconomicRevisionWork {
		t.Helper()
		works, buildErr := builder.BuildEconomicRevisionWork(ctx, observations)
		if buildErr != nil {
			t.Fatalf("build economic work: %v", buildErr)
		}
		for _, work := range works {
			if work.Queue == corebilling.EconomicQueueProvider {
				return work
			}
		}
		t.Fatalf("provider economic work missing: %+v", works)
		return corebilling.EconomicRevisionWork{}
	}
	valuationFor := func(work corebilling.EconomicRevisionWork) economics.Valuation {
		t.Helper()
		normalized, normalizeErr := work.Normalize()
		if normalizeErr != nil {
			t.Fatalf("normalize economic work: %v", normalizeErr)
		}
		identity, identityErr := normalized.Identity()
		if identityErr != nil {
			t.Fatalf("identify economic work: %v", identityErr)
		}
		return economics.Valuation{
			ID: identity.ValuationKey(), Version: economics.ValuationVersionV2,
			Perspective: normalized.Input.Perspective, Basis: normalized.Input.Basis,
			Subject: normalized.Subject, Scope: normalized.Input.Scope,
			InputObservations: append([]metering.ObservationRef(nil), normalized.Input.ObservationRefs...),
			Completeness:      economics.CompletenessPartial, CreatedAt: time.Unix(1_700_052_000, 0).UTC(),
		}
	}
	appendResult := func(work corebilling.EconomicRevisionWork) {
		t.Helper()
		if err := store.AppendEconomicRevisionResult(ctx, work, corebilling.EconomicRevisionResult{Valuation: valuationFor(work)}); err != nil {
			t.Fatalf("append economic result: %v", err)
		}
	}
	current := workFor(a, b)
	superset := workFor(a, b, c)
	subset := workFor(b)
	incomparable := workFor(b, d)
	if current.HeadKey != superset.HeadKey || current.HeadKey != subset.HeadKey || current.HeadKey != incomparable.HeadKey || current.EvidenceRevision != 3 || superset.EvidenceRevision != 3 || subset.EvidenceRevision != 3 || incomparable.EvidenceRevision != 3 {
		t.Fatalf("same-revision work identities differ: current=%+v superset=%+v subset=%+v incomparable=%+v", current, superset, subset, incomparable)
	}

	appendResult(current)
	head, err := store.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, current.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if head.InputSetHash != current.InputSetHash {
		t.Fatalf("initial head hash=%q, want %q", head.InputSetHash, current.InputSetHash)
	}
	appendResult(superset)
	head, err = store.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, current.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if head.InputSetHash != superset.InputSetHash || head.HeadVersion != 2 {
		t.Fatalf("strict superset head=%+v, want hash %q/version 2", head, superset.InputSetHash)
	}
	appendResult(superset)
	headAfterReplay, err := store.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, current.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if headAfterReplay.Fingerprint != head.Fingerprint || headAfterReplay.HeadVersion != head.HeadVersion || headAfterReplay.Fence != head.Fence {
		t.Fatalf("exact superset replay changed head: before=%+v after=%+v", head, headAfterReplay)
	}
	appendResult(subset)
	headAfterSubset, err := store.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, current.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if headAfterSubset.InputSetHash != superset.InputSetHash || headAfterSubset.HeadVersion != head.HeadVersion {
		t.Fatalf("strict subset rolled back head: before=%+v after=%+v", head, headAfterSubset)
	}
	var valuationCountBefore int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, store.storeID).Scan(ctx, &valuationCountBefore); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEconomicRevisionResult(ctx, incomparable, corebilling.EconomicRevisionResult{Valuation: valuationFor(incomparable)}); !errors.Is(err, corebilling.ErrEconomicRevisionFence) {
		t.Fatalf("incomparable same-revision result error=%v, want ErrEconomicRevisionFence", err)
	}
	var valuationCountAfter int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, store.storeID).Scan(ctx, &valuationCountAfter); err != nil {
		t.Fatal(err)
	}
	if valuationCountAfter != valuationCountBefore {
		t.Fatalf("incomparable result partially persisted valuation: before=%d after=%d", valuationCountBefore, valuationCountAfter)
	}
	headAfterIncomparable, err := store.GetEconomicValuationHead(ctx, corebilling.EconomicQueueProvider, current.HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if headAfterIncomparable.InputSetHash != superset.InputSetHash || headAfterIncomparable.HeadVersion != head.HeadVersion || headAfterIncomparable.Fence != head.Fence {
		t.Fatalf("incomparable result mutated head: before=%+v after=%+v", head, headAfterIncomparable)
	}
}

func openRefinement52BillingStore(t *testing.T, storeID string) *DurableStore {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "billing.sqlite")) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = sqlDB.Close()
	})
	return store
}

func openRefinement52Journal(t *testing.T, path, storeID string) *journalstore.DurableStore {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	journal, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = journal.Close()
		_ = sqlDB.Close()
	})
	return journal
}

func refinement52ClosedLeg(storeID string, callID corebilling.BillingCallID) corebilling.CallLegUsageRecord {
	// This authority observation is deliberately sealed into the closed leg by
	// the terminal production boundary. Late evidence can only match these
	// provider-origin account/request/charge identifiers; it cannot self-authorize
	// by repeating them in a late append request.
	authority := refinement52ProviderObservation(storeID, callID, 1, "initial", metering.AcquisitionProviderResponse, metering.OriginProvider, metering.AuthorityObservedClaim)
	return corebilling.CallLegUsageRecord{
		CallID: callID, ALegID: "a-leg", BLegID: "b-leg", AttemptSeq: 1,
		BackendID: "backend-a", ProviderID: "provider-a", ModelID: "model-a",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: corebilling.LegOutcomeWinner, Surfaced: corebilling.SurfacedYes,
		Observations: []metering.Observation{authority},
		Evidence: corebilling.FinalBillingEvidence{
			InputTokens: corebilling.Quantity{Value: 1, Present: true}, OutputTokens: corebilling.Quantity{Value: 1, Present: true},
			Source: corebilling.EvidenceSourceProviderReported, Authority: corebilling.EvidenceAuthorityAuthoritative,
		},
	}
}

func refinement52LateIdentity(storeID string, callID corebilling.BillingCallID) coremetering.LateEconomicIdentity {
	return coremetering.LateEconomicIdentity{
		StoreID: storeID, BillingCallID: callID, ALegID: "a-leg", BLegID: "b-leg", AttemptID: "attempt", AttemptSeq: 1,
		ProviderID: "provider-a", ProviderAccountKey: "provider-account", ProviderRequestID: "request", ProviderChargeID: "charge",
	}
}

func refinement52ProviderObservation(storeID string, callID corebilling.BillingCallID, revision uint64, id, acquisition, origin, authority string) metering.Observation {
	now := time.Unix(1_700_000_000+int64(revision), 0).UTC()
	key := metering.ComponentKey{Direction: metering.DirectionOutput, Component: "vendor:tokens", Unit: metering.UnitToken, SchemaID: "refinement52:v1"}
	value := metering.Decimal{Coefficient: "2", Scale: 0}
	amount := metering.DecimalFromNanoUnits(3)
	observation := metering.Observation{
		Version: metering.ObservationVersionV2, ID: "refinement52-" + id, SourceEventKey: "refinement52-source-" + id, Revision: revision,
		StreamID: "refinement52-stream", Sequence: revision, Origin: origin, Acquisition: acquisition, Authority: authority,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: storeID, TenantID: "tenant", AccountID: "account", ALegID: "a-leg", BillingCallID: callID.String(), CallID: callID.String(), BLegID: "b-leg", AttemptID: "attempt", AttemptSeq: 1, ProviderAccountKey: "provider-account", ProviderRequestID: "request", ProviderChargeID: "charge"},
		Correlation: metering.CorrelationV2{StoreID: storeID, TenantID: "tenant", CallID: callID.String(), BillingCallID: callID.String(), ALegID: "a-leg", BLegID: "b-leg", AttemptID: "attempt", AttemptSeq: 1, ProviderAccountKey: "provider-account", ProviderRequestID: "request", ProviderChargeID: "charge"},
		Semantics:   metering.SemanticsCumulative, ObservedAt: now, ReceivedAt: now, MappingRef: "refinement52:v1",
		Charges: []metering.ReportedCharge{{ChargeItemID: "charge-" + id, Amount: &amount, Currency: "USD", Kind: metering.ChargeKindAggregate, Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator}}},
	}
	if acquisition != metering.AcquisitionProviderFinalizer {
		observation.Measures = []metering.Measure{{Key: key, Value: &value, Quality: metering.QualityObserved}}
	}
	return observation
}

func refinement52StatementObservation(storeID string, callID corebilling.BillingCallID, revision uint64) metering.Observation {
	statement := refinement52ProviderObservation(storeID, callID, revision, "statement", metering.AcquisitionStatementImporter, metering.OriginStatement, metering.AuthorityVerifiedStatement)
	statement.Subject = metering.SubjectRef{
		Kind: metering.SubjectStatementLine, StoreID: storeID, TenantID: "tenant", AccountID: "account",
		ProviderAccountKey: "provider-account",
		StatementID:        "statement-1", StatementLineID: "line-1",
	}
	return statement
}
