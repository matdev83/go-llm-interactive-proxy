package billingstore

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestReservedNanoZeroMigrationBlocksAndClearsReadyResidue(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	accountID := "reserved-residue"
	if err := store.CreateAccount(ctx, billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 100, State: billing.AccountReady, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`UPDATE billing_accounts SET reserved_nano = 40 WHERE account_id = ?`, accountID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetAccount(ctx, accountID); err == nil {
		t.Fatal("GetAccount must fail closed on ready+nonzero reserved_nano before repair")
	}
	repaired, err := getAccountForReconcileTx(ctx, store.db, accountID)
	if err != nil {
		t.Fatalf("reconcile loader: %v", err)
	}
	if repaired.State != billing.AccountReconcileRequired || repaired.ReservedNano != 40 {
		t.Fatalf("reconcile loader account = %+v", repaired)
	}
	if err := reservedNanoZeroUp(ctx, store.db); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReservedNano != 0 || got.State != billing.AccountReconcileRequired {
		t.Fatalf("after migration account = %+v, want reserved=0 reconcile_required", got)
	}
}

func TestSQLiteNewStoresRecordReservedNanoZeroMigration(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM bun_billing_migrations WHERE name = ?`, ReservedNanoZeroMigrationName).Scan(ctx, &count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("migration %s recorded %d times, want 1", ReservedNanoZeroMigrationName, count)
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
	if _, err := store.db.NewRaw(`UPDATE billing_accounts SET currency = '', reserved_nano = 1, state = 'reconcile_required' WHERE account_id = ?`, accountID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := getAccountForReconcileTx(ctx, store.db, accountID)
	if err == nil || errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("corrupt currency must still fail closed: %v", err)
	}
}
