package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestReservedNanoRemovalPreservesCurrentAccountFacts(t *testing.T) {
	t.Parallel()
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

func TestReservedNanoRemovalPreservesReconciliationEventFactsAndProtection(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	accountID := "reserved-event-retired"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 40, State: billing.AccountReady, Version: 2}); err != nil {
		t.Fatal(err)
	}
	var columns int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('billing_reconciliation_events') WHERE name = 'reserved_nano'`).Scan(ctx, &columns); err != nil {
		t.Fatal(err)
	}
	if columns == 0 {
		if _, err := store.db.NewRaw(`ALTER TABLE billing_reconciliation_events ADD COLUMN reserved_nano INTEGER NOT NULL DEFAULT 99`).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.NewRaw(`INSERT INTO billing_reconciliation_events(event_key, account_id, from_state, to_state, first_mismatch_sequence, balance_nano, reserved_nano, spendable_nano, created_at) VALUES (?,?,?,?,?,?,?,?,?)`, "legacy-event", accountID, "reconcile_required", "ready", 17, 40, 99, 40, "2026-08-31T00:00:00Z").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := reservedColumnRemovalSchemaUp(ctx, store.db); err != nil {
		t.Fatalf("remove reconciliation event column: %v", err)
	}
	columns = 0
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('billing_reconciliation_events') WHERE name = 'reserved_nano'`).Scan(ctx, &columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Fatal("reserved_nano must not remain in reconciliation event schema")
	}
	var eventKey, gotAccountID, fromState, toState, createdAt string
	var sequence, balance, spendable int64
	if err := store.db.NewRaw(`SELECT event_key, account_id, from_state, to_state, first_mismatch_sequence, balance_nano, spendable_nano, created_at FROM billing_reconciliation_events WHERE event_key = ?`, "legacy-event").Scan(ctx, &eventKey, &gotAccountID, &fromState, &toState, &sequence, &balance, &spendable, &createdAt); err != nil {
		t.Fatal(err)
	}
	if eventKey != "legacy-event" || gotAccountID != accountID || fromState != "reconcile_required" || toState != "ready" || sequence != 17 || balance != 40 || spendable != 40 || createdAt != "2026-08-31T00:00:00Z" {
		t.Fatalf("reconciliation event facts changed: %q %q %q %q %d %d %d %q", eventKey, gotAccountID, fromState, toState, sequence, balance, spendable, createdAt)
	}
	if _, err := store.db.NewRaw(`UPDATE billing_reconciliation_events SET spendable_nano = 41 WHERE event_key = ?`, "legacy-event").Exec(ctx); err == nil {
		t.Fatal("reconciliation event immutability trigger was lost")
	}
	if _, err := store.db.NewRaw(`INSERT INTO billing_reconciliation_events(event_key, account_id, from_state, to_state, first_mismatch_sequence, balance_nano, spendable_nano, created_at) VALUES (?,?,?,?,?,?,?,?)`, "orphan-event", "missing-account", "ready", "reconcile_required", 0, 0, 0, "2026-08-31T00:00:00Z").Exec(ctx); err == nil {
		t.Fatal("reconciliation event foreign key was lost")
	}
	if err := VerifySchema(ctx, store.db); err != nil {
		t.Fatalf("VerifySchema after reconciliation event retirement: %v", err)
	}
}

func TestCurrentReconciliationWriterOmitsRetiredColumn(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	accountID := "current-reconcile-writer"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 40, State: billing.AccountReady, Version: 2}); err != nil {
		t.Fatal(err)
	}
	var columns int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('billing_reconciliation_events') WHERE name = 'reserved_nano'`).Scan(ctx, &columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		if _, err := store.db.NewRaw(`ALTER TABLE billing_reconciliation_events DROP COLUMN reserved_nano`).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.MarkAccountReconcileRequired(ctx, accountID); err != nil {
		t.Fatalf("current reconciliation writer: %v", err)
	}
	var events int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliation_events WHERE account_id = ? AND to_state = 'reconcile_required'`, accountID).Scan(ctx, &events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("reconciliation event count = %d, want 1", events)
	}
}

func TestSQLiteNewStoresRecordReservedNanoRemovalMigration(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
