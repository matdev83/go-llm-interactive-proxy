//go:build integration

package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// Phase 17.3 B2b1 PostgreSQL parity: same customer settlement ownership fence
// on configured direct PostgreSQL. Skips unless LIP_REQUIRE_POSTGRES=1 and a
// DSN is configured; SQLite suite is authoritative when PG is unavailable.
func TestB2b1PostgresParityWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "b2b1-pg"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	acct := billing.Account{ID: "acct-b2b1-pg", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := testIndependentCallUsageFor(callID, []string{"b-1"})
	call.AccountID = acct.ID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatalf("AppendCallUsage postgres: %v", err)
	}
	exp, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: acct.ID, CallID: callID.String(), Max: billing.Money{Nano: 60, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatalf("AdmitExposure postgres: %v", err)
	}
	res := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 25, Currency: "USD"}, Fingerprint: "b2b1-pg-fp"}
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exp, Result: res})
	if err != nil {
		t.Fatalf("postgres V1 settle: %v", err)
	}
	if settled.Replayed {
		t.Fatalf("postgres first must not be replayed")
	}
	opKey, err := billing.CustomerPostingOperationKey(acct.ID, callID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("postgres pin: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV1 || !pin.IsCompleted() {
		t.Fatalf("postgres pin must be V1 completed, got %#v", pin)
	}
	got, err := store.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 75 {
		t.Fatalf("postgres balance = %d, want 75", got.BalanceNano)
	}
	if err := VerifySchema(ctx, store.DB()); err != nil {
		t.Fatalf("VerifySchema postgres after B2b1: %v", err)
	}
}
