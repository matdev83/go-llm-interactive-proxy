//go:build integration

package billingstore

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// Phase 17.3 F2A PostgreSQL parity: admitted/open V1 calls survive drain and
// terminal handoff on configured direct PostgreSQL. Skips unless
// LIP_REQUIRE_POSTGRES=1 and a DSN is configured; the SQLite suite in
// cutover_admitted_call_f2a_test.go is authoritative when unavailable.

func TestF2AAdmittedCallLifecyclePostgresWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "f2a-pg"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	acct := billing.Account{ID: "acct-f2a-pg", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState:    billing.AccountingCutoverV2Shadow,
			TransitionID: "f2a-pg-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	stub := testIndependentCallUsageFor(callID, []string{"b-pg"})
	stub.AccountID = "acct-f2a-pg"
	if _, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: "acct-f2a-pg", CallID: callID.String(),
		Max:        billing.Money{Nano: 800, Currency: "USD"},
		PricingRef: stub.CustomerPricingRef, ChargePolicyRef: stub.ChargePolicyRef,
	}); err != nil {
		t.Fatalf("AdmitExposure postgres: %v", err)
	}
	opKey, err := billing.CustomerPostingOperationKey("acct-f2a-pg", callID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey); err != nil {
		t.Fatalf("postgres admission pin: %v", err)
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "f2a-pg-drain"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateCutoverV2(ctx, "f2a-pg-early"); !errors.Is(err, billing.ErrCutoverDrainBlocked) {
		t.Fatalf("postgres early activate err = %v, want DrainBlocked", err)
	}
	closure := testIndependentCallUsageFor(callID, []string{"b-pg"})
	closure.AccountID = "acct-f2a-pg"
	if err := store.AppendCallUsage(ctx, closure); err != nil {
		t.Fatalf("postgres terminal closure: %v", err)
	}
	if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(callID, "b-pg")); err != nil {
		t.Fatalf("postgres terminal leg: %v", err)
	}
	custWorker, err := billing.NewCallPostUsageWorkerWithClaim(store, store, f2aRatingStub{charge: 120, fp: "f2a-pg-fp"}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	provWorker, err := billing.NewCallProviderCostWorkerWithClaim(store, store, f2aProviderStub{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("postgres provider worker: %v", err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("postgres customer worker: %v", err)
	}
	if exp, err := store.GetCallExposure(ctx, callID); err != nil {
		t.Fatal(err)
	} else if exp.IsOpen() {
		t.Fatalf("postgres exposure must close")
	}
	if _, err := store.ActivateCutoverV2(ctx, "f2a-pg-activate"); err != nil {
		t.Fatalf("postgres activate after drain: %v", err)
	}
	// New V1 after active fenced; exact replay allowed.
	freshID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	fresh := testIndependentCallUsageFor(freshID, []string{"b-new"})
	fresh.AccountID = "acct-f2a-pg"
	if err := store.AppendCallUsage(ctx, fresh); err == nil {
		t.Fatalf("postgres new V1 in v2_active must be fenced")
	}
	got, err := store.GetCallUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsage(ctx, got); err != nil {
		t.Fatalf("postgres exact replay in v2_active: %v", err)
	}
	if err := VerifySchema(ctx, store.DB()); err != nil {
		t.Fatalf("VerifySchema postgres after F2A: %v", err)
	}
}
