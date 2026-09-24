package billingstore

// Phase 17.3 F4 RED: ordinary completed provider history must not create a
// phantom financial_adjustment pin.
//
// Seed: one completed provider-cost revision (head + completed provider_charge
// pin) with no pending selected-cost/cost-pass-through/direct adjustment.
// Drain via production BeginCutoverDraining/Classify/Activate.
// Correct behavior: no financial_adjustment pin invented, drain ready,
// activation succeeds. Pre-fix: classifier scans every provider-cost head and
// invents a pinned financial_adjustment pin that blocks activation forever.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func f4NewStore(t *testing.T, storeID string) *DurableStore {
	t.Helper()
	base := newSQLiteTestStore(t)
	if base.StoreID() == storeID {
		return base
	}
	s, err := NewDurableStore(context.Background(), base.DB(), Config{StoreID: storeID})
	if err != nil {
		t.Fatalf("NewDurableStore %q: %v", storeID, err)
	}
	return s
}

func f4EnsureShadow(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState:    billing.AccountingCutoverV2Shadow,
		TransitionID: "f4-shadow",
	}); err != nil {
		t.Fatal(err)
	}
}

func f4ProviderRevisionInput(storeID, accountID string, callID billing.BillingCallID, headKey string, revision uint64, amount int64) billing.ProviderCostRevisionInput {
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: storeID, AccountID: accountID,
		ALegID: "a-f4", BillingCallID: callID.String(), BLegID: "b-f4",
	}
	amountDecimal := metering.DecimalFromNanoUnits(amount)
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	evidence := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: subject, Scope: "f4-provider-cost", Payer: payer,
		Observations: []metering.Observation{{
			Version: metering.ObservationVersionV2, ID: fmt.Sprintf("f4-provider-charge-%d", revision),
			SourceEventKey: fmt.Sprintf("f4-provider-charge-%d", revision), Revision: revision,
			StreamID: "f4-provider-stream", Sequence: revision, Origin: metering.OriginProvider,
			Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
			Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
			Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
			Correlation: metering.CorrelationV2{
				StoreID: subject.StoreID, ALegID: subject.ALegID,
				BillingCallID: subject.BillingCallID, BLegID: subject.BLegID,
			},
			Semantics: metering.SemanticsCumulative, ObservedAt: time.Unix(44, 0).UTC(),
			ReceivedAt: time.Unix(44, 0).UTC(), MappingRef: "f4.provider.cost",
			Charges: []metering.ReportedCharge{{
				ChargeItemID: "provider-charge", Kind: metering.ChargeKindAggregate,
				Amount: &amountDecimal, Currency: "USD", Payer: payer,
			}},
		}},
		Rater: economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "f4-rater", Version: "v1"}, RaterID: "reference"},
	}
	cost := billing.OperatorCOGSResult{
		KnownSubtotalByCurrency: map[string]billing.Money{"USD": {Nano: amount, Currency: "USD"}},
		KnownSubtotal:           billing.Money{Nano: amount, Currency: "USD"},
		Completeness:            billing.CostCompletenessKnown,
		Payable:                 true,
		IncludedLegKeys:         []string{"b-f4"},
	}
	return billing.ProviderCostRevisionInput{
		AccountID: accountID, CallID: callID, Subject: subject, HeadKey: headKey,
		EvidenceRevision: revision, InputSetHash: fmt.Sprintf("%064x", revision),
		ValuationID: fmt.Sprintf("f4-provider-valuation-%d", revision), Cost: cost,
		Authoritative: true, Evidence: evidence,
	}
}

func f4CountFinancialAdjustmentPins(t *testing.T, store *DurableStore, status billing.PostingPinStatus) int {
	t.Helper()
	var n int
	if err := store.DB().NewRaw(`SELECT COUNT(1) FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND status = ?`,
		store.StoreID(), string(billing.PostingOperationFinancialAdjustment), string(status)).Scan(context.Background(), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestF4OrdinaryProviderHistoryCreatesNoPhantomAdjustment(t *testing.T) {
	t.Parallel()
	store := f4NewStore(t, "f4-phantom")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-f4-phantom", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	f4EnsureShadow(t, store)
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	headKey := "f4-head-1"
	posted, err := store.ApplyProviderCostRevision(ctx, f4ProviderRevisionInput(store.StoreID(), acct.ID, callID, headKey, 1, 10))
	if err != nil {
		t.Fatalf("seed provider revision: %v", err)
	}
	if !posted.Applied {
		t.Fatalf("seed revision must apply, got %+v", posted)
	}
	// Ordinary history: completed provider_charge pin exists.
	var providerPinned, providerCompleted int
	if err := store.DB().NewRaw(`SELECT COUNT(1) FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND status = ?`,
		store.StoreID(), string(billing.PostingOperationProviderCharge), string(billing.PostingPinCompleted)).Scan(ctx, &providerCompleted); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().NewRaw(`SELECT COUNT(1) FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND status = ?`,
		store.StoreID(), string(billing.PostingOperationProviderCharge), string(billing.PostingPinPinned)).Scan(ctx, &providerPinned); err != nil {
		t.Fatal(err)
	}
	if providerCompleted == 0 {
		t.Fatalf("seed must leave completed provider_charge pin (completed=%d pinned=%d)", providerCompleted, providerPinned)
	}
	// No pending selected-cost/cost-pass-through/direct adjustment: zero
	// financial_adjustment pins before drain.
	if n := f4CountFinancialAdjustmentPins(t, store, billing.PostingPinPinned); n != 0 {
		t.Fatalf("pre-drain pinned financial_adjustment pins = %d, want 0 (no pending adjustment)", n)
	}
	if n := f4CountFinancialAdjustmentPins(t, store, billing.PostingPinCompleted); n != 0 {
		t.Fatalf("pre-drain completed financial_adjustment pins = %d, want 0 (no adjustment history)", n)
	}
	// Drain through production APIs.
	if _, _, err := store.BeginCutoverDraining(ctx, "f4-drain"); err != nil {
		t.Fatalf("BeginCutoverDraining: %v", err)
	}
	// No phantom: classification must not invent a pinned financial_adjustment
	// for the historical provider head.
	if n := f4CountFinancialAdjustmentPins(t, store, billing.PostingPinPinned); n != 0 {
		t.Fatalf("F4 phantom: pinned financial_adjustment pins = %d, want 0 (historical head is not pending work)", n)
	}
	status, err := store.CutoverDrainStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Counts.AdjustmentPending != 0 {
		t.Fatalf("F4 phantom: AdjustmentPending = %d, want 0", status.Counts.AdjustmentPending)
	}
	if !status.ReadyForActivation {
		t.Fatalf("F4 phantom: drain not ready after ordinary provider history: %+v (phantom blocks activation)", status.Counts)
	}
	if _, err := store.ActivateCutoverV2(ctx, "f4-activate"); err != nil {
		t.Fatalf("F4 phantom: ActivateCutoverV2 blocked after ordinary history: %v", err)
	}
}

func TestF4MultipleHeadsBatchesNoPhantom(t *testing.T) {
	t.Parallel()
	store := f4NewStore(t, "f4-multi")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-f4-multi", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	f4EnsureShadow(t, store)
	const total = 7
	for i := range total {
		callID, err := billing.NewBillingCallID()
		if err != nil {
			t.Fatal(err)
		}
		headKey := fmt.Sprintf("f4-multi-head-%d", i)
		if _, err := store.ApplyProviderCostRevision(ctx, f4ProviderRevisionInput(store.StoreID(), acct.ID, callID, headKey, 1, 10)); err != nil {
			t.Fatalf("seed history %d: %v", i, err)
		}
	}
	// Bounded small-batch drain must not invent phantoms for any head.
	if _, _, err := store.BeginCutoverDraining(ctx, "f4-multi-drain"); err != nil {
		t.Fatalf("BeginCutoverDraining: %v", err)
	}
	if _, err := store.ClassifyCutoverDraining(ctx, 2); err != nil {
		t.Fatalf("bounded classify batch=2: %v", err)
	}
	if _, err := store.ClassifyCutoverDraining(ctx, 2); err != nil {
		t.Fatalf("bounded re-classify batch=2: %v", err)
	}
	if n := f4CountFinancialAdjustmentPins(t, store, billing.PostingPinPinned); n != 0 {
		t.Fatalf("F4 phantom: pinned financial_adjustment = %d, want 0 for %d historical heads", n, total)
	}
	status, err := store.CutoverDrainStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Counts.AdjustmentPending != 0 || status.Counts.V1Pinned != 0 {
		t.Fatalf("F4: counts must be empty for historical heads: %+v", status.Counts)
	}
	if !status.ReadyForActivation {
		t.Fatalf("F4: drain must be ready for %d historical heads: %+v", total, status.Counts)
	}
	if _, err := store.ActivateCutoverV2(ctx, "f4-multi-activate"); err != nil {
		t.Fatalf("F4: activation after %d historical heads: %v", total, err)
	}
}

func TestF4ReopenPreservesNoPhantomActivation(t *testing.T) {
	t.Parallel()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", filepath.ToSlash(filepath.Join(t.TempDir(), "f4-reopen.db")))
	open := func(storeID string) (*DurableStore, func()) {
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(4)
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
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
		return s, func() { _ = s.Close() }
	}
	store, closeFn := open("f4-reopen")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-f4-reopen", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f4-reopen-shadow"}); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		callID, err := billing.NewBillingCallID()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ApplyProviderCostRevision(ctx, f4ProviderRevisionInput(store.StoreID(), acct.ID, callID, fmt.Sprintf("f4-reopen-head-%d", i), 1, 10)); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "f4-reopen-drain"); err != nil {
		t.Fatal(err)
	}
	closeFn()
	// Same-file reopen at drain boundary: marker stays draining, no phantom.
	reopened, close2 := open("f4-reopen")
	defer close2()
	got, err := reopened.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != billing.AccountingCutoverV1Draining {
		t.Fatalf("reopen state = %q, want v1_draining", got.State)
	}
	if _, err := reopened.ClassifyCutoverDraining(ctx, 2); err != nil {
		t.Fatalf("reopen classify: %v", err)
	}
	var pinned int
	if err := reopened.DB().NewRaw(`SELECT COUNT(1) FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND status = ?`,
		reopened.StoreID(), string(billing.PostingOperationFinancialAdjustment), string(billing.PostingPinPinned)).Scan(ctx, &pinned); err != nil {
		t.Fatal(err)
	}
	if pinned != 0 {
		t.Fatalf("F4 phantom after reopen: pinned = %d, want 0", pinned)
	}
	status, err := reopened.CutoverDrainStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.ReadyForActivation {
		t.Fatalf("reopen drain must be ready: %+v", status.Counts)
	}
	activated, err := reopened.ActivateCutoverV2(ctx, "f4-reopen-activate")
	if err != nil {
		t.Fatalf("reopen activate: %v", err)
	}
	if activated.State != billing.AccountingCutoverV2Active {
		t.Fatalf("activated = %q, want v2_active", activated.State)
	}
	// Same-file reopen after activation preserves v2_active.
	close2()
	reopened2, close3 := open("f4-reopen")
	defer close3()
	got2, err := reopened2.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got2.State != billing.AccountingCutoverV2Active {
		t.Fatalf("post-activate reopen state = %q, want v2_active", got2.State)
	}
}
