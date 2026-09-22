//go:build integration

package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// Phase 17.3 B2b3 PostgreSQL parity: same financial adjustment ownership fence
// on configured direct PostgreSQL. Skips unless LIP_REQUIRE_POSTGRES=1 and a
// DSN is configured; SQLite suite is authoritative when PG is unavailable.
func TestB2b3PostgresParityWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "b2b3-pg"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	acct := billing.Account{ID: "acct-b2b3-pg", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.ParseBillingCallID("bc_00000000000000000000000000000c11")
	if err != nil {
		t.Fatal(err)
	}
	subject := b2b3Subject(store.StoreID(), acct.ID, callID.String())
	headKey := "b2b3-pg-head"
	val := b2b3Valuation(t, "b2b3-pg-val-1", 1, 10_000_000_000)
	applied, err := store.ApplySelectedCostAdjustment(ctx, b2b3AdjustmentInput(store, acct.ID, callID, headKey, subject, billing.SelectedCostHeadExpectation{}, val))
	if err != nil {
		t.Fatalf("postgres V1 adjustment post: %v", err)
	}
	if applied.Status != billing.SelectedCostTransitionApplied {
		t.Fatalf("postgres status = %q, want applied", applied.Status)
	}
	opKey, err := billing.FinancialAdjustmentPostingOperationKey(store.StoreID(), acct.ID, callID, headKey, subject)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationFinancialAdjustment, opKey)
	if err != nil {
		t.Fatalf("postgres adjustment pin: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV1 || !pin.IsCompleted() {
		t.Fatalf("postgres pin must be V1 completed, got %#v", pin)
	}
	if n := b2b3AdjustmentJournals(t, store, acct.ID); n != 1 {
		t.Fatalf("postgres journals = %d, want 1", n)
	}
	if err := VerifySchema(ctx, store.DB()); err != nil {
		t.Fatalf("VerifySchema postgres after B2b3: %v", err)
	}
}
