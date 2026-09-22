//go:build integration

package billingstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// Phase 17.3 B2b4 PostgreSQL parity: same synchronous ownership fence on
// configured direct PostgreSQL. Skips unless LIP_REQUIRE_POSTGRES=1 and a DSN
// is configured; SQLite suite is authoritative when PG is unavailable.
func TestB2b4PostgresParityWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "b2b4-pg"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// Cost pass-through leg.
	costAcct := billing.Account{ID: "acct-b2b4-pg-cost", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, costAcct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: costAcct.ID, ALegID: "a-b2b4-pg",
		SessionID: "session-b2b4-pg", StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(), Outcome: billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: costAcct.ID, CallID: callID.String(), Max: billing.Money{Nano: 100, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := billing.CostPassThroughPolicy{
		MissingCost: billing.CostPassThroughMissingCostProvisional,
		SafeBound:   &billing.Money{Nano: 100, Currency: "USD"}, AllowLateAdjustment: true,
	}
	state := billing.CostPassThroughSettlement{
		PolicyRef: call.ChargePolicyRef, Policy: policy, Status: billing.CostPassThroughSettlementProvisional,
		SafeBound: billing.Money{Nano: 100, Currency: "USD"}, PostedAmount: billing.Money{Nano: 100, Currency: "USD"},
	}
	initial := billing.CallRatingResult{CallID: call.CallID, CustomerCharge: state.PostedAmount, Fingerprint: "b2b4-pg-provisional", CostPassThrough: &state}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: initial}); err != nil {
		t.Fatal(err)
	}
	provider := billing.CostPassThroughProviderCost{
		LURKey: "lur-b2b4-pg", ValuationID: "val-b2b4-pg", Revision: 2,
		InputHash:     strings.Repeat("d", 64),
		Amount:        billing.Money{Nano: 80, Currency: "USD"},
		AmountPresent: true, Reconciled: true, Authoritative: true,
	}
	applied, err := store.ApplyCostPassThroughRevision(ctx, billing.CostPassThroughRevisionInput{AccountID: costAcct.ID, CallID: callID, ProviderCost: provider})
	if err != nil {
		t.Fatalf("postgres cost post: %v", err)
	}
	if !applied.Applied {
		t.Fatalf("postgres cost must apply")
	}
	opKey, err := billing.CostPassThroughFinancialAdjustmentPostingOperationKey(store.StoreID(), costAcct.ID, callID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationFinancialAdjustment, opKey)
	if err != nil {
		t.Fatalf("postgres cost pin: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV1 || !pin.IsCompleted() {
		t.Fatalf("postgres cost pin must be V1 completed, got %#v", pin)
	}
	// Direct leg.
	directAcct := billing.Account{ID: "acct-b2b4-pg-direct", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, directAcct); err != nil {
		t.Fatal(err)
	}
	adj := billing.AdjustmentInput{AccountID: directAcct.ID, Amount: billing.Money{Nano: 100, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "b2b4-pg-1", Reason: "correction"}
	if _, err := store.PostAdjustment(ctx, adj); err != nil {
		t.Fatalf("postgres direct post: %v", err)
	}
	directKey, err := billing.DirectFinancialAdjustmentPostingOperationKey(store.StoreID(), directAcct.ID, "b2b4-pg-1")
	if err != nil {
		t.Fatal(err)
	}
	dpin, err := store.GetPostingPin(ctx, billing.PostingOperationFinancialAdjustment, directKey)
	if err != nil {
		t.Fatalf("postgres direct pin: %v", err)
	}
	if dpin.Owner != billing.PostingOwnerV1 || !dpin.IsCompleted() {
		t.Fatalf("postgres direct pin must be V1 completed, got %#v", dpin)
	}
	if err := VerifySchema(ctx, store.DB()); err != nil {
		t.Fatalf("VerifySchema postgres after B2b4: %v", err)
	}
}
