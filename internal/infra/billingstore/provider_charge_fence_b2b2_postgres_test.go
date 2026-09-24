//go:build integration

package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// Phase 17.3 B2b2 PostgreSQL parity: same provider charge ownership fence on
// configured direct PostgreSQL. Skips unless LIP_REQUIRE_POSTGRES=1 and a DSN
// is configured; SQLite suite is authoritative when PG is unavailable.
func TestB2b2PostgresParityWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "b2b2-pg"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	acct := billing.Account{ID: "acct-b2b2-pg", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
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
	leg := testIndependentCallLegFor(callID, "b-1")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatalf("AppendCallLegUsage postgres: %v", err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	result := billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 11, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
	posting, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: acct.ID, CallID: callID, Leg: leg, Result: result})
	if err != nil {
		t.Fatalf("postgres V1 provider post: %v", err)
	}
	if posting.Replayed {
		t.Fatalf("postgres first must not be replayed")
	}
	opKey, err := billing.ProviderCostSourceKey(sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		t.Fatalf("postgres provider pin: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV1 || !pin.IsCompleted() {
		t.Fatalf("postgres pin must be V1 completed, got %#v", pin)
	}
	txs, err := store.JournalTransactions(ctx, acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tx := range txs {
		if tx.OperationKind == "provider_call_cogs" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("postgres provider journals = %d, want 1", n)
	}
	if err := VerifySchema(ctx, store.DB()); err != nil {
		t.Fatalf("VerifySchema postgres after B2b2: %v", err)
	}
}
