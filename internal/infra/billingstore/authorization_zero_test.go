package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteZeroExposureAuthorizationRetainsHoldWithoutFinancialMutation(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "zero-auth", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 10, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authorize(ctx, authorizationInput("zero-auth", "turn", "zero-id", 0))
	if err != nil {
		t.Fatal(err)
	}
	if auth.Before.SpendableNano != 10 || auth.After.SpendableNano != 10 || auth.After.ReservedNano != 0 {
		t.Fatalf("zero snapshots = before %+v after %+v", auth.Before, auth.After)
	}
	account, err := store.GetAccount(ctx, "zero-auth")
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedNano != 0 || account.Version != 2 {
		t.Fatalf("zero account = %+v", account)
	}
	journals, err := store.JournalTransactions(ctx, "zero-auth")
	if err != nil {
		t.Fatal(err)
	}
	if len(journals) != 0 {
		t.Fatalf("zero authorization must not create positive journal entries: %d", len(journals))
	}
}
