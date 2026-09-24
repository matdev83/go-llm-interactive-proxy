package billingstore

// Phase 17.3 B2b3 RED: financial adjustment posting-time ownership fence.
// Scope: financial_adjustment only (selected-cost corrections via
// ApplySelectedCostAdjustment). Customer/provider already fenced B2b1/B2b2.
//
// Invariant under test (must FAIL before B2b3, PASS after):
// - Every selected-cost monetary adjustment uses its canonical head/subject
//   identity to derive FinancialAdjustmentPostingOperationKey (no weak caller).
// - V1 active/shadow default owner V1; draining permits only preclassified/
//   pinned V1 with matching marker metadata; new commands fenced.
// - v2_active permits V2 only; stale V1 wake/retry fenced unless exact replay.
// - Pin + journal/balance/head/linkage effects commit atomically in same tx.
// - Replacement chains keep immutable linkage, one authority per revision.

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func b2b3NewStore(t *testing.T, storeID string) *DurableStore {
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

func b2b3Subject(storeID, accountID, callRaw string) metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: storeID, AccountID: accountID,
		ALegID: "a-b2b3", BillingCallID: callRaw, BLegID: "b-b2b3",
	}
}

func b2b3Valuation(t *testing.T, valuationID string, revision uint64, nanos int64) billing.SelectedCostValuation {
	t.Helper()
	decimal := metering.DecimalFromNanoUnits(nanos)
	v, err := billing.NewSelectedCostValuation(billing.SelectedCostValuationRef{
		ValuationID: valuationID, Revision: revision, InputSetHash: "0000000000000000000000000000000000000000000000000000000000000001",
	}, billing.OperatorCostSelectionResult{
		Status: billing.OperatorCostSelectionStatusFinal, Provenance: billing.OperatorCostProvenanceAttempted,
		Currency: "USD", Amount: &billing.MonetaryExactAmount{Currency: "USD", Decimal: &decimal},
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func b2b3SetupAccount(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	acct := billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(context.Background(), acct); err != nil {
		t.Fatal(err)
	}
}

func b2b3EnsureShadow(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState:    billing.AccountingCutoverV2Shadow,
		TransitionID: "b2b3-shadow",
	}); err != nil {
		t.Fatal(err)
	}
}

func b2b3AdjustmentJournals(t *testing.T, store *DurableStore, accountID string) int {
	t.Helper()
	txs, err := store.JournalTransactions(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tx := range txs {
		if tx.OperationKind == "provider_call_cogs" {
			n++
		}
	}
	return n
}

func TestB2b3REDV1DefaultBindsFinancialAdjustmentPin(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b3-red-default")
	ctx := context.Background()
	b2b3SetupAccount(t, store, "acct-b2b3-red-default")
	callID, err := billing.ParseBillingCallID("bc_00000000000000000000000000000b23")
	if err != nil {
		t.Fatal(err)
	}
	subject := b2b3Subject(store.StoreID(), "acct-b2b3-red-default", callID.String())
	input := billing.SelectedCostAdjustmentInput{
		AccountID: "acct-b2b3-red-default", CallID: callID, HeadKey: "b2b3-red-head",
		Subject: subject, Expected: billing.SelectedCostHeadExpectation{},
		Selected: b2b3Valuation(t, "b2b3-red-val-1", 1, 10_000_000_000),
	}
	applied, err := store.ApplySelectedCostAdjustment(ctx, input)
	if err != nil {
		t.Fatalf("default adjustment: %v", err)
	}
	if applied.Status != billing.SelectedCostTransitionApplied {
		t.Fatalf("status = %q, want applied", applied.Status)
	}
	opKey, err := billing.FinancialAdjustmentPostingOperationKey(store.StoreID(), "acct-b2b3-red-default", callID, "b2b3-red-head", subject)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationFinancialAdjustment, opKey)
	if err != nil {
		t.Fatalf("RED: financial_adjustment pin missing for %q: %v (no B2b3 atomic ownership fence)", opKey, err)
	}
	if pin.Owner != billing.PostingOwnerV1 || !pin.IsCompleted() {
		t.Fatalf("RED: default pin must be V1 completed, got %#v", pin)
	}
}

func TestB2b3REDShadowV1PostsWithPin(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b3-red-shadow")
	ctx := context.Background()
	b2b3EnsureShadow(t, store)
	b2b3SetupAccount(t, store, "acct-b2b3-red-shadow")
	callID, err := billing.ParseBillingCallID("bc_00000000000000000000000000000b24")
	if err != nil {
		t.Fatal(err)
	}
	subject := b2b3Subject(store.StoreID(), "acct-b2b3-red-shadow", callID.String())
	input := billing.SelectedCostAdjustmentInput{
		AccountID: "acct-b2b3-red-shadow", CallID: callID, HeadKey: "b2b3-red-shadow-head",
		Subject: subject, Expected: billing.SelectedCostHeadExpectation{},
		Selected: b2b3Valuation(t, "b2b3-red-shadow-val-1", 1, 10_000_000_000),
	}
	if _, err := store.ApplySelectedCostAdjustment(ctx, input); err != nil {
		t.Fatalf("shadow V1 adjustment: %v", err)
	}
	opKey, err := billing.FinancialAdjustmentPostingOperationKey(store.StoreID(), "acct-b2b3-red-shadow", callID, "b2b3-red-shadow-head", subject)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetPostingPin(ctx, billing.PostingOperationFinancialAdjustment, opKey); err != nil {
		t.Fatalf("RED: shadow pin missing: %v (no B2b3 fence)", err)
	}
}

func TestB2b3REDDrainingUnpinnedBlockedZeroEffects(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b3-red-drain")
	ctx := context.Background()
	b2b3EnsureShadow(t, store)
	b2b3SetupAccount(t, store, "acct-b2b3-red-drain")
	callID, err := billing.ParseBillingCallID("bc_00000000000000000000000000000b25")
	if err != nil {
		t.Fatal(err)
	}
	subject := b2b3Subject(store.StoreID(), "acct-b2b3-red-drain", callID.String())
	if _, _, err := store.BeginCutoverDraining(ctx, "b2b3-red-drain-go"); err != nil {
		t.Fatal(err)
	}
	opKey, err := billing.FinancialAdjustmentPostingOperationKey(store.StoreID(), "acct-b2b3-red-drain", callID, "b2b3-red-drain-head", subject)
	if err != nil {
		t.Fatal(err)
	}
	// Remove any classified pin to prove new draining work is fenced.
	_, _ = store.DB().NewRaw(`DELETE FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND operation_key = ?`,
		store.StoreID(), string(billing.PostingOperationFinancialAdjustment), opKey).Exec(ctx)
	before := b2b3AdjustmentJournals(t, store, "acct-b2b3-red-drain")
	input := billing.SelectedCostAdjustmentInput{
		AccountID: "acct-b2b3-red-drain", CallID: callID, HeadKey: "b2b3-red-drain-head",
		Subject: subject, Expected: billing.SelectedCostHeadExpectation{},
		Selected: b2b3Valuation(t, "b2b3-red-drain-val-1", 1, 10_000_000_000),
	}
	if _, err := store.ApplySelectedCostAdjustment(ctx, input); err == nil {
		t.Fatalf("RED: draining unpinned adjustment must be fenced, got success (missing B2b3 fence)")
	}
	if n := b2b3AdjustmentJournals(t, store, "acct-b2b3-red-drain"); n != before {
		t.Fatalf("RED: fenced posting wrote journals %d -> %d, want 0 new", before, n)
	}
}

func TestB2b3REDV2ActiveV1Blocked(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b3-red-v2active")
	ctx := context.Background()
	b2b3SetupAccount(t, store, "acct-b2b3-red-v2active")
	callID, err := billing.ParseBillingCallID("bc_00000000000000000000000000000b26")
	if err != nil {
		t.Fatal(err)
	}
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "b2b3-red-v2-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	dr, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: sh.Version, ExpectedEpoch: sh.Epoch, NextState: billing.AccountingCutoverV1Draining, TransitionID: "b2b3-red-v2-drain"})
	if err != nil {
		t.Fatal(err)
	}
	_ = dr
	cur, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: cur.Version, ExpectedEpoch: cur.Epoch, NextState: billing.AccountingCutoverV2Active, TransitionID: "b2b3-red-v2-active"}); err != nil {
		t.Fatal(err)
	}
	subject := b2b3Subject(store.StoreID(), "acct-b2b3-red-v2active", callID.String())
	input := billing.SelectedCostAdjustmentInput{
		AccountID: "acct-b2b3-red-v2active", CallID: callID, HeadKey: "b2b3-red-v2-head",
		Subject: subject, Expected: billing.SelectedCostHeadExpectation{},
		Selected: b2b3Valuation(t, "b2b3-red-v2-val-1", 1, 10_000_000_000),
	}
	if _, err := store.ApplySelectedCostAdjustment(ctx, input); err == nil {
		t.Fatalf("RED: v2_active V1 adjustment must fail closed (missing B2b3 fence)")
	}
}
