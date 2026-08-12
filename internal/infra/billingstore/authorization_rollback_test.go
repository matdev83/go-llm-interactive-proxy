package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteAuthorizationRollbackLeavesNoHoldOnJournalFailure(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "rollback-auth", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 50, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	input := authorizationInput("rollback-auth", "turn", "fault", 5)
	// Occupy the deterministic authorization journal transaction ID without
	// creating its hold, forcing the later journal insert to fail after the hold
	// INSERT has executed inside the same transaction.
	holdKey, err := billing.AuthorizationHoldKey(input.AccountID, input.TURKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.postJournalTransaction(ctx, billing.JournalTransaction{
		ID: authorizationTransactionID(holdKey), AccountID: "rollback-auth", Book: billing.JournalBookFinancial,
		Currency: "USD", SourceKey: "fault-source", Entries: []billing.JournalEntry{
			{LedgerAccount: "cash", Side: billing.JournalDebit, Amount: billing.Money{Nano: 1, Currency: "USD"}},
			{LedgerAccount: "customer", Side: billing.JournalCredit, Amount: billing.Money{Nano: 1, Currency: "USD"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	sealed, err := input.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.authorizeAttempt(ctx, input, sealed); err == nil {
		t.Fatal("expected journal identity failure")
	}
	account, err := store.GetAccount(ctx, "rollback-auth")
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedNano != 0 || account.Version != 2 {
		t.Fatalf("rollback account = %+v, want reserved 0/version 2", account)
	}
	var holds int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM authorization_holds WHERE account_id = ?`, "rollback-auth").Scan(ctx, &holds); err != nil {
		t.Fatal(err)
	}
	if holds != 0 {
		t.Fatalf("rollback left %d authorization holds", holds)
	}
}
