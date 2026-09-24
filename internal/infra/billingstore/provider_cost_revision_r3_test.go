package billingstore

// Phase 17.3 R3: terminal disposition and exact replay for stale/excluded
// provider revisions (requirements 10.6/14.4).
//
// A. Older queued revision processed after newer head must retire its pin;
//    drain converges and activation proceeds.
// B. Candidate-subset/tie-break stale loser uses distinct branch, same terminal.
// C. Exact ignored/excluded commits no-money pin; crash before queue completion
//    replays and completes the durable job with zero extra journals.
// D. Conflicting replay against completed no-money pin fails closed.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	_ "modernc.org/sqlite"
)

func r3OpenFileStore(t *testing.T, dsn, storeID string) *DurableStore {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	seedTestSchemaIfEmpty(t, bunDB)
	s, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	return s
}

// r3RevisionInput builds a payable/nonpayable revision input with explicit
// hash/valuation control for pin-identity tests. Amounts map to IncludedLegKeys.
func r3RevisionInput(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, bLegID, headKey string, revision uint64, amount int64, payable bool, inputHash, valuationID string) billing.ProviderCostRevisionInput {
	t.Helper()
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: store.StoreID(), AccountID: accountID,
		ALegID: "a-f2b", BillingCallID: callID.String(), BLegID: bLegID,
	}
	amountDecimal := metering.DecimalFromNanoUnits(amount)
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	if !payable {
		payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer"}
	}
	evidence := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: subject, Scope: "r3-provider-cost", Payer: payer,
		Observations: []metering.Observation{{
			Version: metering.ObservationVersionV2, ID: "r3-obs-" + bLegID + "-rev", SourceEventKey: "r3-obs-" + bLegID + "-rev", Revision: revision,
			StreamID: "r3-stream", Sequence: revision, Origin: metering.OriginProvider,
			Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
			Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
			Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
			Correlation: metering.CorrelationV2{StoreID: subject.StoreID, ALegID: subject.ALegID, BillingCallID: subject.BillingCallID, BLegID: subject.BLegID},
			Semantics:   metering.SemanticsCumulative, ObservedAt: time.Unix(100, 0).UTC(), ReceivedAt: time.Unix(100, 0).UTC(),
			MappingRef: "r3.provider.cost",
			Charges:    []metering.ReportedCharge{{ChargeItemID: "provider-charge", Kind: metering.ChargeKindAggregate, Amount: &amountDecimal, Currency: "USD", Payer: payer}},
		}},
		Rater: economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "r3-rater", Version: "v1"}, RaterID: "reference"},
	}
	cost := billing.OperatorCOGSResult{
		KnownSubtotalByCurrency: map[string]billing.Money{"USD": {Nano: amount, Currency: "USD"}},
		KnownSubtotal:           billing.Money{Nano: amount, Currency: "USD"},
		Completeness:            billing.CostCompletenessKnown,
		Payable:                 payable,
		IncludedLegKeys:         []string{bLegID},
	}
	if !payable {
		cost.IncludedLegKeys = nil
	}
	return billing.ProviderCostRevisionInput{
		AccountID: accountID, CallID: callID, Subject: subject, HeadKey: headKey,
		EvidenceRevision: revision, InputSetHash: inputHash,
		ValuationID: valuationID, Cost: cost,
		Authoritative: payable, Evidence: evidence,
	}
}

// r3ProviderChargeRevision builds a provider-charge revision for the same
// B-leg execution (execution-authority exclusion branch).
func r3ProviderChargeRevision(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, bLegID, chargeID, headKey string, revision uint64, amount int64, valuationID, inputHash string) billing.ProviderCostRevisionInput {
	t.Helper()
	subject := metering.SubjectRef{
		Kind: metering.SubjectProviderCharge, StoreID: store.StoreID(), AccountID: accountID,
		ALegID: "a-f2b", BillingCallID: callID.String(), BLegID: bLegID,
		ProviderAccountKey: "provider-account-r3", ProviderChargeID: chargeID,
	}
	amountDecimal := metering.DecimalFromNanoUnits(amount)
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	obsSubject := subject
	evidence := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: subject, Scope: "r3-provider-cost", Payer: payer,
		Observations: []metering.Observation{{
			Version: metering.ObservationVersionV2, ID: "r3-charge-obs-" + chargeID, SourceEventKey: "r3-charge-evt-" + chargeID, Revision: revision,
			StreamID: "r3-stream", Sequence: revision, Origin: metering.OriginProvider,
			Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
			Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
			Lifecycle: metering.LifecycleBackendAttempt, Subject: obsSubject,
			Correlation: metering.CorrelationV2{StoreID: subject.StoreID, ALegID: subject.ALegID, BillingCallID: subject.BillingCallID, BLegID: subject.BLegID, ProviderAccountKey: subject.ProviderAccountKey, ProviderChargeID: chargeID},
			Semantics:   metering.SemanticsCumulative, ObservedAt: time.Unix(100, 0).UTC(), ReceivedAt: time.Unix(100, 0).UTC(),
			MappingRef: "r3.provider.charge",
			Charges:    []metering.ReportedCharge{{ChargeItemID: "provider-charge-item-" + chargeID, Kind: metering.ChargeKindAggregate, Amount: &amountDecimal, Currency: "USD", Payer: payer}},
		}},
		Rater: economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "r3-rater", Version: "v1"}, RaterID: "reference"},
	}
	cost := billing.OperatorCOGSResult{
		KnownSubtotalByCurrency: map[string]billing.Money{"USD": {Nano: amount, Currency: "USD"}},
		KnownSubtotal:           billing.Money{Nano: amount, Currency: "USD"},
		Completeness:            billing.CostCompletenessKnown,
		Payable:                 true,
		IncludedLegKeys:         []string{bLegID},
	}
	return billing.ProviderCostRevisionInput{
		AccountID: accountID, CallID: callID, Subject: subject, HeadKey: headKey,
		EvidenceRevision: revision, InputSetHash: inputHash,
		ValuationID: valuationID, Cost: cost,
		Authoritative: true, Evidence: evidence,
	}
}

func r3PinStatus(t *testing.T, store *DurableStore, kind billing.PostingOperationKind, key string) billing.PostingPin {
	t.Helper()
	pin, err := store.GetPostingPin(context.Background(), kind, key)
	if err != nil {
		t.Fatalf("GetPostingPin %q: %v", key, err)
	}
	return pin
}

func r3PinnedCount(t *testing.T, store *DurableStore) int {
	t.Helper()
	var n int
	if err := store.DB().NewRaw(`SELECT COUNT(1) FROM billing_posting_ownership_pins WHERE store_id = ? AND status = ?`,
		store.StoreID(), string(billing.PostingPinPinned)).Scan(context.Background(), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func r3EconomicPending(t *testing.T, store *DurableStore) int {
	t.Helper()
	var n int
	if err := store.DB().NewRaw(`SELECT COUNT(1) FROM billing_economic_revision_work_state WHERE store_id = ? AND provider_posting = 1 AND status IN ('pending','processing')`,
		store.StoreID()).Scan(context.Background(), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A. Older queued revision behind newer head drains via real worker.
func TestR3_StaleQueuedRevisionDrainsViaWorker(t *testing.T) {
	t.Parallel()
	store := f2bNewStore(t, "r3-a-stale-drain")
	ctx := f2bSetupShadowAccount(t, store, "acct-r3-a")
	callID := f2bMustCallID(t)
	const bLeg = "b-r3-a"
	const head = "r3-a-head"

	// Queue older revision 1 (retryable, pending).
	work1 := f2bProviderWork(t, store, "acct-r3-a", callID, bLeg, head, 1, true)
	if err := store.AppendEconomicRevisionWork(ctx, work1); err != nil {
		t.Fatalf("append rev1 work: %v", err)
	}
	// Post newer head revision 2 directly (same head/call/B-leg, higher rev).
	rev2 := r3RevisionInput(t, store, "acct-r3-a", callID, bLeg, head, 2, 300, true,
		"1111111111111111111111111111111111111111111111111111111111111111", "r3-a-val-2")
	posted, err := store.ApplyProviderCostRevision(ctx, rev2)
	if err != nil {
		t.Fatalf("post rev2 head: %v", err)
	}
	if !posted.Applied {
		t.Fatalf("rev2 must apply, got %+v", posted)
	}
	journalsBefore := f2bProviderJournals(t, store, "acct-r3-a")

	// Enter draining: classification pins rev1.
	_, status, err := store.BeginCutoverDraining(ctx, "r3-a-drain")
	if err != nil {
		t.Fatalf("begin draining: %v", err)
	}
	if status.ReadyForActivation {
		t.Fatalf("drain must block with pending rev1")
	}
	_, _, rev1Key, err := billing.MonetaryEconomicPostingKey(store.StoreID(), work1)
	if err != nil {
		t.Fatal(err)
	}
	if pin := r3PinStatus(t, store, billing.PostingOperationProviderCharge, rev1Key); pin.Status != billing.PostingPinPinned {
		t.Fatalf("rev1 pin must be pinned after classify, got %#v", pin)
	}

	// Real worker processes older queued revision 1 -> Stale terminal.
	worker := f2bProviderWorker(t, store)
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("worker rev1 stale: %v", err)
	}
	// Queue terminal: no pending monetary work.
	if n := r3EconomicPending(t, store); n != 0 {
		t.Fatalf("economic pending=%d want 0 (queue terminal)", n)
	}
	// Pin terminal: rev1 completed as stale/superseded, no stranded pin.
	pinAfter := r3PinStatus(t, store, billing.PostingOperationProviderCharge, rev1Key)
	if !pinAfter.IsCompleted() {
		t.Fatalf("rev1 pin must be completed after stale worker, got %#v (drain would block forever)", pinAfter)
	}
	if n := r3PinnedCount(t, store); n != 0 {
		t.Fatalf("pinned=%d want 0 (no stranded pin)", n)
	}
	// No new money for stale.
	if n := f2bProviderJournals(t, store, "acct-r3-a"); n != journalsBefore {
		t.Fatalf("journals=%d want %d (stale posts no money)", n, journalsBefore)
	}
	// Exact replay of stale succeeds idempotently.
	work1Norm, err := work1.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	valProbe, err := store.LoadEconomicRevisionValuation(ctx, func() billing.EconomicRevisionIdentity {
		id, err := work1Norm.Identity()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}())
	if err != nil {
		t.Fatalf("valuation must exist for replay probe: %v", err)
	}
	_ = valProbe
	rebuilt, err := billing.BuildProviderCostRevisionInput(work1Norm, valProbe)
	if err != nil {
		t.Fatalf("rebuild rev1 input: %v", err)
	}
	staleReplay, err := store.ApplyProviderCostRevision(ctx, rebuilt)
	if err != nil {
		t.Fatalf("exact stale replay must succeed: %v", err)
	}
	if !staleReplay.Stale {
		t.Fatalf("replay must stay stale, got %+v", staleReplay)
	}
	// Drain converges; activation can proceed.
	if _, err := store.ActivateCutoverV2(ctx, "r3-a-activate"); err != nil {
		t.Fatalf("activate after stale drain: %v", err)
	}
}

func r3PersistValuationForInput(t *testing.T, store *DurableStore, input billing.ProviderCostRevisionInput) {
	t.Helper()
	ctx := context.Background()
	refs := make([]metering.ObservationRef, 0, len(input.Evidence.Observations))
	for _, obs := range input.Evidence.Observations {
		ref, err := obs.Ref(input.Subject.StoreID)
		if err != nil {
			t.Fatalf("valuation ref: %v", err)
		}
		refs = append(refs, ref)
	}
	// Include explicit ObservationRefs when evidence carries refs-only payload.
	refs = append(refs, input.Evidence.ObservationRefs...)
	val := economics.Valuation{
		ID: input.ValuationID, Version: economics.ValuationVersionV2,
		Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: input.Subject, Scope: "r3-b-cost", InputObservations: refs,
		Payer:        metering.PaymentParty{Kind: metering.PaymentPartyOperator},
		Completeness: economics.CompletenessPartial,
		CreatedAt:    time.Unix(100, 0).UTC(),
	}
	if err := store.AppendValuation(ctx, val); err != nil {
		t.Fatalf("persist valuation %q: %v", val.ID, err)
	}
}

// B. Same-revision candidate-subset loser is terminal with pin disposition.
func TestR3_SameRevisionSubsetLoserIsTerminal(t *testing.T) {
	t.Parallel()
	store := f5f7NewStore(t, "r3-b-subset")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-r3-b", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID := f2bMustCallID(t)
	const head = "r3-b-head"
	const bLeg = "b-r3-b"

	// Head rev1 with two observations (amounts 10 + 15 = 25).
	headInput := r3TwoObservationInput(t, store, acct.ID, callID, bLeg, head, 1, 10, 15)
	r3PersistValuationForInput(t, store, headInput)
	headRes, err := store.ApplyProviderCostRevision(ctx, headInput)
	if err != nil {
		t.Fatalf("head 2-obs: %v", err)
	}
	if !headRes.Applied {
		t.Fatalf("head must apply, got %+v", headRes)
	}
	journalsBefore := f5f7ProviderJournals(t, store, acct.ID)

	// Candidate same rev1 with only first observation (subset) -> stale loser.
	candidate := r3SingleObservationInput(t, store, acct.ID, callID, bLeg, head, 1, 10, "r3-b-subset-obs-1")
	stale, err := store.ApplyProviderCostRevision(ctx, candidate)
	if err != nil {
		t.Fatalf("subset candidate must be terminal success, err=%v", err)
	}
	if !stale.Stale {
		t.Fatalf("subset loser must be stale, got %+v", stale)
	}
	candKey, err := billing.ProviderRevisionPostingOperationKey(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if pin := r3PinStatus(t, store, billing.PostingOperationProviderCharge, candKey); !pin.IsCompleted() {
		t.Fatalf("subset loser pin must be completed, got %#v", pin)
	}
	if n := f5f7ProviderJournals(t, store, acct.ID); n != journalsBefore {
		t.Fatalf("journals=%d want %d (subset posts no money)", n, journalsBefore)
	}
	// Exact replay succeeds.
	replay, err := store.ApplyProviderCostRevision(ctx, candidate)
	if err != nil {
		t.Fatalf("subset exact replay: %v", err)
	}
	if !replay.Stale {
		t.Fatalf("subset replay must stay stale, got %+v", replay)
	}
}

// Tie-break loser: same revision, same evidence refs (equal), smaller hash loses.
func TestR3_SameRevisionTieBreakLoserIsTerminal(t *testing.T) {
	t.Parallel()
	store := f5f7NewStore(t, "r3-b-tiebreak")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-r3-b-tie", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID := f2bMustCallID(t)
	const head = "r3-b-tie-head"
	const bLeg = "b-r3-b-tie"

	// Head with explicit larger hash wins first.
	headInput := r3RevisionInput(t, store, acct.ID, callID, bLeg, head, 1, 20, true,
		"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "r3-b-tie-val-head")
	r3PersistValuationForInput(t, store, headInput)
	if _, err := store.ApplyProviderCostRevision(ctx, headInput); err != nil {
		t.Fatalf("tie head: %v", err)
	}
	journalsBefore := f5f7ProviderJournals(t, store, acct.ID)
	// Candidate same rev, same observations, smaller hash -> tie-break loser.
	// Reuse head evidence exactly so refs are equal; only hash/valuation differ.
	candidate := headInput
	candidate.InputSetHash = "0000000000000000000000000000000000000000000000000000000000000001"
	candidate.ValuationID = "r3-b-tie-val-loser"
	stale, err := store.ApplyProviderCostRevision(ctx, candidate)
	if err != nil {
		t.Fatalf("tie-break loser must be terminal success, err=%v", err)
	}
	if !stale.Stale {
		t.Fatalf("tie-break loser must be stale, got %+v", stale)
	}
	candKey, err := billing.ProviderRevisionPostingOperationKey(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if pin := r3PinStatus(t, store, billing.PostingOperationProviderCharge, candKey); !pin.IsCompleted() {
		t.Fatalf("tie-break loser pin must be completed, got %#v", pin)
	}
	if n := f5f7ProviderJournals(t, store, acct.ID); n != journalsBefore {
		t.Fatalf("journals=%d want %d (tie-break posts no money)", n, journalsBefore)
	}
	replay, err := store.ApplyProviderCostRevision(ctx, candidate)
	if err != nil {
		t.Fatalf("tie-break exact replay: %v", err)
	}
	if !replay.Stale {
		t.Fatalf("tie-break replay must stay stale, got %+v", replay)
	}
}

func r3TwoObservationInput(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, bLegID, headKey string, revision uint64, amount1, amount2 int64) billing.ProviderCostRevisionInput {
	t.Helper()
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: store.StoreID(), AccountID: accountID,
		ALegID: "a-f2b", BillingCallID: callID.String(), BLegID: bLegID,
	}
	dec1 := metering.DecimalFromNanoUnits(amount1)
	dec2 := metering.DecimalFromNanoUnits(amount2)
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	mkObs := func(id string, amount metering.Decimal, seq uint64) metering.Observation {
		return metering.Observation{
			Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id, Revision: revision,
			StreamID: "r3-b-stream", Sequence: seq, Origin: metering.OriginProvider,
			Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
			Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
			Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
			Correlation: metering.CorrelationV2{StoreID: subject.StoreID, ALegID: subject.ALegID, BillingCallID: subject.BillingCallID, BLegID: subject.BLegID},
			Semantics:   metering.SemanticsCumulative, ObservedAt: time.Unix(100, 0).UTC(), ReceivedAt: time.Unix(100, 0).UTC(),
			MappingRef: "r3.b.cost",
			Charges:    []metering.ReportedCharge{{ChargeItemID: "provider-charge-" + id, Kind: metering.ChargeKindAggregate, Amount: &amount, Currency: "USD", Payer: payer}},
		}
	}
	obs1 := mkObs("r3-b-subset-obs-1", dec1, 1)
	obs2 := mkObs("r3-b-subset-obs-2", dec2, 2)
	evidence := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: subject, Scope: "r3-b-cost", Payer: payer,
		Observations: []metering.Observation{obs1, obs2},
		Rater:        economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "r3-b-rater", Version: "v1"}, RaterID: "reference"},
	}
	total := amount1 + amount2
	cost := billing.OperatorCOGSResult{
		KnownSubtotalByCurrency: map[string]billing.Money{"USD": {Nano: total, Currency: "USD"}},
		KnownSubtotal:           billing.Money{Nano: total, Currency: "USD"},
		Completeness:            billing.CostCompletenessKnown,
		Payable:                 true,
		IncludedLegKeys:         []string{bLegID},
	}
	return billing.ProviderCostRevisionInput{
		AccountID: accountID, CallID: callID, Subject: subject, HeadKey: headKey,
		EvidenceRevision: revision, InputSetHash: "",
		ValuationID: "r3-b-head-val", Cost: cost,
		Authoritative: true, Evidence: evidence,
	}
}

func r3SingleObservationInput(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, bLegID, headKey string, revision uint64, amount int64, obsID string) billing.ProviderCostRevisionInput {
	t.Helper()
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: store.StoreID(), AccountID: accountID,
		ALegID: "a-f2b", BillingCallID: callID.String(), BLegID: bLegID,
	}
	dec := metering.DecimalFromNanoUnits(amount)
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	obs := metering.Observation{
		Version: metering.ObservationVersionV2, ID: obsID, SourceEventKey: obsID, Revision: revision,
		StreamID: "r3-b-stream", Sequence: 1, Origin: metering.OriginProvider,
		Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
		Correlation: metering.CorrelationV2{StoreID: subject.StoreID, ALegID: subject.ALegID, BillingCallID: subject.BillingCallID, BLegID: subject.BLegID},
		Semantics:   metering.SemanticsCumulative, ObservedAt: time.Unix(100, 0).UTC(), ReceivedAt: time.Unix(100, 0).UTC(),
		MappingRef: "r3.b.cost",
		Charges:    []metering.ReportedCharge{{ChargeItemID: "provider-charge-" + obsID, Kind: metering.ChargeKindAggregate, Amount: &dec, Currency: "USD", Payer: payer}},
	}
	evidence := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: subject, Scope: "r3-b-cost", Payer: payer,
		Observations: []metering.Observation{obs},
		Rater:        economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "r3-b-rater", Version: "v1"}, RaterID: "reference"},
	}
	cost := billing.OperatorCOGSResult{
		KnownSubtotalByCurrency: map[string]billing.Money{"USD": {Nano: amount, Currency: "USD"}},
		KnownSubtotal:           billing.Money{Nano: amount, Currency: "USD"},
		Completeness:            billing.CostCompletenessKnown,
		Payable:                 true,
		IncludedLegKeys:         []string{bLegID},
	}
	return billing.ProviderCostRevisionInput{
		AccountID: accountID, CallID: callID, Subject: subject, HeadKey: headKey,
		EvidenceRevision: revision, InputSetHash: "",
		ValuationID: "r3-b-cand-val", Cost: cost,
		Authoritative: true, Evidence: evidence,
	}
}

// C. Execution-authority exclusion crash/reopen replays via worker with zero extra money.
func TestR3_ExcludedExecutionCrashReplaysViaWorker(t *testing.T) {
	t.Parallel()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "r3-c-excl.db")) + "?_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	const storeID = "r3-c-excl"
	const accountID = "acct-r3-c"
	store := r3OpenFileStore(t, dsn, storeID)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureAccountingCutover(ctx); err != nil {
		t.Fatal(err)
	}
	callID := f2bMustCallID(t)
	const bLeg = "b-r3-c"
	const bLegHead = "r3-c-bleg-head"
	const chargeHead = "r3-c-charge-head"
	const chargeID = "ch-r3-c"

	// B-leg payable owns the execution first.
	blegInput := r3RevisionInput(t, store, accountID, callID, bLeg, bLegHead, 1, 200, true,
		"2222222222222222222222222222222222222222222222222222222222222222", "r3-c-bleg-val")
	if _, err := store.ApplyProviderCostRevision(ctx, blegInput); err != nil {
		t.Fatalf("b-leg owner: %v", err)
	}

	// Provider-charge child for same B-leg execution is excluded (same B-leg lineage).
	exclInput := r3ProviderChargeRevision(t, store, accountID, callID, bLeg, chargeID, chargeHead, 1, 200,
		"r3-c-charge-val", "3333333333333333333333333333333333333333333333333333333333333333")
	exclRes, err := store.ApplyProviderCostRevision(ctx, exclInput)
	if err != nil {
		t.Fatalf("execution exclusion must be terminal success: %v", err)
	}
	if !exclRes.Ignored {
		t.Fatalf("execution exclusion must be ignored, got %+v", exclRes)
	}
	exclKey, err := billing.ProviderRevisionPostingOperationKey(exclInput)
	if err != nil {
		t.Fatal(err)
	}
	if pin := r3PinStatus(t, store, billing.PostingOperationProviderCharge, exclKey); !pin.IsCompleted() {
		t.Fatalf("exclusion pin must be completed, got %#v", pin)
	}
	journalsBefore := f5f7ProviderJournals(t, store, accountID)

	// Simulate crash before economic queue completion: enqueue equivalent
	// provider-charge economic work, drive claim+rate+persist+post, then close
	// without completing the queue item.
	chargeWork := r3ProviderChargeWork(t, store, accountID, callID, bLeg, chargeID, "r3-c-queue-head", 1)
	if err := store.AppendEconomicRevisionWork(ctx, chargeWork); err != nil {
		t.Fatalf("append charge work: %v", err)
	}
	// Claim + rate + persist + post (worker steps up to queue completion).
	// Use a millisecond lease so the simulated crash (close without complete)
	// leaves an expired processing lease that the reopened worker can reclaim
	// deterministically without wall-clock waiting for a 30s lease.
	claim, claimed, err := store.ClaimEconomicRevisionWork(ctx, chargeWork, "r3-c-worker", time.Millisecond)
	if err != nil || !claimed {
		t.Fatalf("claim charge work: claimed=%v err=%v", claimed, err)
	}
	_ = claim
	workNorm, err := chargeWork.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := workNorm.Identity()
	if err != nil {
		t.Fatal(err)
	}
	valuation, err := f2bRater{}.Rate(ctx, workNorm.Input.Clone())
	if err != nil {
		t.Fatal(err)
	}
	valuation, err = billing.NormalizeRevisionValuationForWork(workNorm, identity, valuation)
	if err != nil {
		t.Fatalf("normalize valuation: %v", err)
	}
	if err := store.AppendEconomicRevisionResult(ctx, workNorm, billing.EconomicRevisionResult{Valuation: valuation}); err != nil {
		t.Fatalf("persist result: %v", err)
	}
	// Post via worker seam (excluded: no money, completes its own pin).
	// Use a fresh worker bound to the same store for production posting path.
	workerPre := f2bProviderWorker(t, store)
	_ = workerPre
	postInput, err := billing.BuildProviderCostRevisionInput(workNorm, valuation)
	if err != nil {
		t.Fatalf("build post input: %v", err)
	}
	postRes, err := store.ApplyProviderCostRevision(ctx, postInput)
	if err != nil {
		t.Fatalf("queue exclusion post must succeed: %v", err)
	}
	if !postRes.Ignored && !postRes.Stale {
		t.Fatalf("queue exclusion must be ignored/stale, got %+v", postRes)
	}
	// Let the millisecond processing lease expire so reopen reclaim is
	// deterministic, then CRASH: close without CompleteEconomicRevisionWork.
	time.Sleep(10 * time.Millisecond)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen same DB file; worker replay must complete the durable job.
	reopened := r3OpenFileStore(t, dsn, storeID)
	t.Cleanup(func() { _ = reopened.Close() })
	worker := f2bProviderWorker(t, reopened)
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("reopen worker replay must succeed: %v", err)
	}
	// Second pass converges (no perpetual retry).
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("second replay pass: %v", err)
	}
	if n := r3EconomicPending(t, reopened); n != 0 {
		t.Fatalf("economic pending=%d want 0 after crash replay", n)
	}
	if n := f5f7ProviderJournals(t, reopened, accountID); n != journalsBefore {
		t.Fatalf("journals=%d want %d (zero extra for exclusion replay)", n, journalsBefore)
	}
}

// r3ProviderChargeWork builds queueable provider-charge economic work for the
// same B-leg execution (excluded when B-leg aggregate owns execution).
func r3ProviderChargeWork(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, bLegID, chargeID, headKey string, revision uint64) billing.EconomicRevisionWork {
	t.Helper()
	subject := metering.SubjectRef{
		Kind: metering.SubjectProviderCharge, StoreID: store.StoreID(), AccountID: accountID,
		ALegID: "a-f2b", BillingCallID: callID.String(), BLegID: bLegID,
		ProviderAccountKey: "provider-account-r3", ProviderChargeID: chargeID,
	}
	amount := metering.DecimalFromNanoUnits(200)
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	obs := metering.Observation{
		Version: metering.ObservationVersionV2, ID: "r3-c-queue-obs", SourceEventKey: "r3-c-queue-obs", Revision: revision,
		StreamID: "r3-c-stream", Sequence: revision, Origin: metering.OriginProvider,
		Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
		Correlation: metering.CorrelationV2{StoreID: subject.StoreID, ALegID: subject.ALegID, BillingCallID: subject.BillingCallID, BLegID: subject.BLegID, ProviderAccountKey: subject.ProviderAccountKey, ProviderChargeID: chargeID},
		Semantics:   metering.SemanticsCumulative, ObservedAt: time.Unix(100, 0).UTC(), ReceivedAt: time.Unix(100, 0).UTC(),
		MappingRef: "r3.c.queue",
		Charges:    []metering.ReportedCharge{{ChargeItemID: "provider-charge-item-" + chargeID, Kind: metering.ChargeKindAggregate, Amount: &amount, Currency: "USD", Payer: payer}},
	}
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: obs.Subject, Scope: "b_leg", Payer: payer, Observations: []metering.Observation{obs},
	}
	work := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, HeadKey: headKey, Subject: obs.Subject,
		EvidenceRevision: revision, Input: input, CreatedAt: time.Unix(250, 0).UTC(),
	}
	normalized, err := work.Normalize()
	if err != nil {
		t.Fatalf("charge work normalize: %v", err)
	}
	return normalized
}

// D. Conflicting replay against completed no-money pin fails closed.
func TestR3_ConflictingReplayAgainstCompletedExclusionFails(t *testing.T) {
	t.Parallel()
	store := f5f7NewStore(t, "r3-d-conflict")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-r3-d", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID := f2bMustCallID(t)
	const bLeg = "b-r3-d"
	const bLegHead = "r3-d-bleg-head"
	const chargeHead = "r3-d-charge-head"
	const chargeID = "ch-r3-d"

	blegInput := r3RevisionInput(t, store, acct.ID, callID, bLeg, bLegHead, 1, 200, true,
		"4444444444444444444444444444444444444444444444444444444444444444", "r3-d-bleg-val")
	if _, err := store.ApplyProviderCostRevision(ctx, blegInput); err != nil {
		t.Fatalf("b-leg owner: %v", err)
	}
	excl := r3ProviderChargeRevision(t, store, acct.ID, callID, bLeg, chargeID, chargeHead, 1, 200,
		"r3-d-charge-val", "5555555555555555555555555555555555555555555555555555555555555555")
	if _, err := store.ApplyProviderCostRevision(ctx, excl); err != nil {
		t.Fatalf("exclusion seed: %v", err)
	}
	journalsBefore := f5f7ProviderJournals(t, store, acct.ID)

	// Same immutable revision identity (same rev+hash => same pin) but different
	// fingerprint (different ValuationID) must fail closed, never mutate.
	conflict := excl
	conflict.ValuationID = "r3-d-charge-val-CONFLICT"
	if _, err := store.ApplyProviderCostRevision(ctx, conflict); err == nil {
		t.Fatalf("conflicting replay must fail closed (same pin, different outcome)")
	}
	if n := f5f7ProviderJournals(t, store, acct.ID); n != journalsBefore {
		t.Fatalf("journals=%d want %d (conflict posts no money)", n, journalsBefore)
	}
	// Exact replay still succeeds.
	exact, err := store.ApplyProviderCostRevision(ctx, excl)
	if err != nil {
		t.Fatalf("exact replay must succeed: %v", err)
	}
	if !exact.Ignored {
		t.Fatalf("exact replay must stay ignored, got %+v", exact)
	}
}
