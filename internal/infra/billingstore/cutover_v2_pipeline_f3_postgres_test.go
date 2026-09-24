//go:build integration

package billingstore

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// Phase 17.3 F3 PostgreSQL parity: legal empty activation followed by a fresh
// V2 pipeline on configured direct PostgreSQL. Skips unless
// LIP_REQUIRE_POSTGRES=1 and a DSN is configured; the SQLite suite in
// cutover_v2_pipeline_f3_test.go is authoritative when unavailable.

func TestF3V2PipelinePostgresWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "f3-pg"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	acct := billing.Account{ID: "acct-f3-pg", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 90000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	// Explicit V2 rejected pre-active.
	preCall, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	preStub := testIndependentCallUsageFor(preCall, []string{"b-pre"})
	preStub.AccountID = "acct-f3-pg"
	if _, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-f3-pg", CallID: preCall.String(),
		Max:        billing.Money{Nano: 50, Currency: "USD"},
		PricingRef: preStub.CustomerPricingRef, ChargePolicyRef: preStub.ChargePolicyRef,
	}, billing.PostingOwnerV2); !errors.Is(err, billing.ErrCutoverV2NotAuthorized) {
		t.Fatalf("postgres preactive V2 err = %v, want NotAuthorized", err)
	}
	// Legal empty activation via coordinator.
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f3-pg-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "f3-pg-drain"); err != nil {
		t.Fatal(err)
	}
	activated, err := store.ActivateCutoverV2(ctx, "f3-pg-activate")
	if err != nil {
		t.Fatalf("postgres legal empty activation: %v", err)
	}
	if activated.State != billing.AccountingCutoverV2Active {
		t.Fatalf("activated = %q, want v2_active", activated.State)
	}
	// Fresh V2 admission + terminal + claims + settlement exactly once.
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	closure := testIndependentCallUsageFor(callID, []string{"b-pg"})
	closure.AccountID = "acct-f3-pg"
	exp, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-f3-pg", CallID: callID.String(),
		Max:        billing.Money{Nano: 700, Currency: "USD"},
		PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
	}, billing.PostingOwnerV2)
	if err != nil {
		t.Fatalf("postgres V2 admit: %v", err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, closure, billing.PostingOwnerV2); err != nil {
		t.Fatalf("postgres V2 closure: %v", err)
	}
	if err := store.AppendCallLegUsageWithOwner(ctx, testIndependentCallLegFor(callID, "b-pg"), billing.PostingOwnerV2); err != nil {
		t.Fatalf("postgres V2 leg: %v", err)
	}
	claimedProv, err := store.ClaimProviderCostWorkWithCutover(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimedProv) != 1 || claimedProv[0].Claim.Owner != billing.PostingOwnerV2 {
		t.Fatalf("postgres provider claim = %+v, want one V2", claimedProv)
	}
	claimedCust, err := store.ClaimCompleteCallsWithCutover(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimedCust) != 1 || claimedCust[0].Claim.Owner != billing.PostingOwnerV2 {
		t.Fatalf("postgres customer claim = %+v, want one V2", claimedCust)
	}
	// Explicit claims lease the work, so settlement consumes those tokens
	// directly through production posting seams (same authority cutover
	// workers consume). Workers afterwards must replay without second money.
	legForPost := testIndependentCallLegFor(callID, "b-pg")
	sealedForPost, err := legForPost.Seal()
	if err != nil {
		t.Fatal(err)
	}
	provCopy := claimedProv[0].Claim
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{
		AccountID: "acct-f3-pg", CallID: callID, Leg: legForPost,
		Result:       billing.OperatorCostResult{LURKey: sealedForPost.Key, Amount: billing.Money{Nano: 9, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true},
		PostingOwner: provCopy.Owner, Claim: &provCopy,
	}); err != nil {
		t.Fatalf("postgres V2 provider post with claim: %v", err)
	}
	durableForCust, err := store.GetCallUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	custCopy := claimedCust[0].Claim
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: durableForCust, Exposure: exp,
		Result:       billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 80, Currency: "USD"}, Fingerprint: "f3-pg-fp"},
		PostingOwner: custCopy.Owner, Claim: &custCopy,
	}); err != nil {
		t.Fatalf("postgres V2 customer post with claim: %v", err)
	}
	got, err := store.GetAccount(ctx, "acct-f3-pg")
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 90000-80 {
		t.Fatalf("postgres balance = %d, want %d", got.BalanceNano, 90000-80)
	}
	// Legacy V1 remains fenced in active.
	freshV1, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	freshStub := testIndependentCallUsageFor(freshV1, []string{"b-v1"})
	freshStub.AccountID = "acct-f3-pg"
	if _, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: "acct-f3-pg", CallID: freshV1.String(),
		Max:        billing.Money{Nano: 10, Currency: "USD"},
		PricingRef: freshStub.CustomerPricingRef, ChargePolicyRef: freshStub.ChargePolicyRef,
	}); err == nil {
		t.Fatalf("postgres V1 in active must be fenced")
	}
	if err := VerifySchema(ctx, store.DB()); err != nil {
		t.Fatalf("VerifySchema postgres after F3: %v", err)
	}
	_ = exp
}
