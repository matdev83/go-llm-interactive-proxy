package billingstore

// Phase 17.3 B2b4 RED: synchronous monetary adjustment writers must bind
// atomic financial_adjustment ownership pins (Task 17.3 one posting version
// per logical adjustment). Gate-only (cutoverGateForNewV1) does not bind
// owner/epoch or atomically complete replay.
//
// Invariant under test (must FAIL before B2b4, PASS after):
// - ApplyCostPassThroughRevision/Adjustment posts a completed
//   financial_adjustment pin in the same transaction as balance/journal/head.
// - PostAdjustment posts a completed financial_adjustment pin in the same
//   transaction as balance/journal.
// - Exact replay backfills/completes the same owner pin with zero new money.
// - Conflicting owner/epoch/amount/currency/source fails before effects.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func b2b4CountFinancialAdjustmentPins(t *testing.T, store *DurableStore) int {
	t.Helper()
	var n int
	if err := store.DB().NewRaw(`SELECT COUNT(1) FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ?`,
		store.StoreID(), string(billing.PostingOperationFinancialAdjustment)).Scan(context.Background(), &n); err != nil {
		t.Fatalf("count adjustment pins: %v", err)
	}
	return n
}

func b2b4SetupCostPassThroughHead(t *testing.T, store *DurableStore, ctx context.Context, account billing.Account) (billing.CallUsageRecord, billing.CostPassThroughProviderCost) {
	t.Helper()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID, ALegID: "a-b2b4",
		SessionID: "session-b2b4", StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(), Outcome: billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 100, Currency: account.Currency},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := billing.CostPassThroughPolicy{
		MissingCost: billing.CostPassThroughMissingCostProvisional,
		SafeBound:   &billing.Money{Nano: 100, Currency: account.Currency}, AllowLateAdjustment: true,
	}
	state := billing.CostPassThroughSettlement{
		PolicyRef: call.ChargePolicyRef, Policy: policy, Status: billing.CostPassThroughSettlementProvisional,
		SafeBound: billing.Money{Nano: 100, Currency: account.Currency}, PostedAmount: billing.Money{Nano: 100, Currency: account.Currency},
	}
	initial := billing.CallRatingResult{CallID: call.CallID, CustomerCharge: state.PostedAmount, Fingerprint: "b2b4-provisional", CostPassThrough: &state}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: initial}); err != nil {
		t.Fatal(err)
	}
	provider := billing.CostPassThroughProviderCost{
		LURKey: "lur-b2b4", ValuationID: "val-b2b4-2", Revision: 2,
		InputHash:     strings.Repeat("b", 64),
		Amount:        billing.Money{Nano: 80, Currency: "USD"},
		AmountPresent: true, Reconciled: true, Authoritative: true,
	}
	return call, provider
}

func TestB2b4REDCostPassThroughBindsFinancialAdjustmentPin(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b4-red-cost-pass")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-b2b4-red-cost-pass", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 200, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	call, provider := b2b4SetupCostPassThroughHead(t, store, ctx, acct)
	applied, err := store.ApplyCostPassThroughRevision(ctx, billing.CostPassThroughRevisionInput{AccountID: acct.ID, CallID: call.CallID, ProviderCost: provider})
	if err != nil {
		t.Fatalf("cost pass-through revision: %v", err)
	}
	if !applied.Applied {
		t.Fatalf("revision must apply, got %+v", applied)
	}
	if n := b2b4CountFinancialAdjustmentPins(t, store); n != 1 {
		t.Fatalf("RED: cost pass-through posted %d financial_adjustment pins, want 1 (no B2b4 atomic ownership fence)", n)
	}
}

func TestB2b4REDDirectAdjustmentBindsFinancialAdjustmentPin(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b4-red-direct")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-b2b4-red-direct", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	adj := billing.AdjustmentInput{AccountID: acct.ID, Amount: billing.Money{Nano: 100, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "b2b4-red-adj-1", Reason: "correction"}
	if _, err := store.PostAdjustment(ctx, adj); err != nil {
		t.Fatalf("PostAdjustment: %v", err)
	}
	if n := b2b4CountFinancialAdjustmentPins(t, store); n != 1 {
		t.Fatalf("RED: PostAdjustment posted %d financial_adjustment pins, want 1 (no B2b4 atomic ownership fence)", n)
	}
}
