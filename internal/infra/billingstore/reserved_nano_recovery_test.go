package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestReservedNanoRemovalPreservesCurrentAccountFacts(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	accountID := "reserved-retired"
	account := billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPostpaid, CreditLimit: 100, BalanceNano: 25, State: billing.AccountReady, Version: 7}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	var columns int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('billing_accounts') WHERE name = 'reserved_nano'`).Scan(ctx, &columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Fatal("reserved_nano must not remain in the current account schema")
	}
	got, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if got != account {
		t.Fatalf("account changed across migration: got=%+v want=%+v", got, account)
	}
}

func TestSQLiteNewStoresRecordReservedNanoRemovalMigration(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM bun_billing_migrations WHERE name = ?`, ReservedColumnRemovalMigrationName).Scan(ctx, &count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("migration %s recorded %d times, want 1", ReservedColumnRemovalMigrationName, count)
	}
	if err := VerifySchema(ctx, store.db); err != nil {
		t.Fatalf("VerifySchema: %v", err)
	}
}

func TestGetAccountForReconcileTxRejectsCorruptCurrency(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	accountID := "reserved-corrupt-currency"
	if err := store.CreateAccount(ctx, billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 10, State: billing.AccountReady, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`UPDATE billing_accounts SET currency = '', state = 'reconcile_required' WHERE account_id = ?`, accountID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := getAccountForReconcileTx(ctx, store.db, accountID)
	if err == nil || err == ErrAccountNotFound {
		t.Fatalf("corrupt currency must still fail closed: %v", err)
	}
}
