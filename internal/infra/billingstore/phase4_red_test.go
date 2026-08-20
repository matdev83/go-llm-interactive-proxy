package billingstore

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestCurrentReadersExcludeHistoricalAuthorizationAndAuditReaderPreservesIt(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "phase4-audit", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 3}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	_, err := store.db.NewRaw(`INSERT INTO journal_transactions(
 transaction_id, account_id, book, currency, source_key, semantic_fingerprint,
 turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
 correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
 reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano,
 credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"legacy-auth-1", account.ID, "authorization", account.Currency, "legacy-auth", "legacy-fingerprint",
		"", "", "", int64(1), "", "", "", "legacy_authorization", int64(100), int64(100), int64(12), int64(0), int64(100), int64(100), int64(0), int64(0), "prepaid", uint64(2), uint64(3), "2026-08-31T00:00:00Z").Exec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for ordinal, entry := range []struct {
		account string
		side    string
	}{
		{"customer_reserved_exposure", "debit"}, {"authorization_contra", "credit"},
	} {
		if _, err := store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`, "legacy-auth-1", ordinal, entry.account, entry.side, "USD", 12).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	current, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 0 {
		t.Fatalf("current journal reader returned historical authorization rows: %+v", current)
	}
	historical, err := store.HistoricalAuthorizationJournals(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(historical) != 1 || historical[0].ReservedBeforeNano != 12 || len(historical[0].Entries) != 2 {
		t.Fatalf("historical authorization audit = %+v", historical)
	}
	report, err := store.AccountReport(ctx, account.ID, billing.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Transactions) != 0 {
		t.Fatalf("account report returned authorization rows: %+v", report.Transactions)
	}
}

func TestCurrentJournalWriterRejectsAuthorizationBook(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	input := billing.JournalTransaction{ID: "auth-write", Book: billing.JournalBook("authorization"), Currency: "USD", SourceKey: "auth-write", AccountID: "missing", Entries: []billing.JournalEntry{
		{LedgerAccount: "reserved", Side: billing.JournalDebit, Amount: billing.Money{Nano: 1, Currency: "USD"}},
		{LedgerAccount: "contra", Side: billing.JournalCredit, Amount: billing.Money{Nano: 1, Currency: "USD"}},
	}}
	if _, err := store.postJournalTransaction(context.Background(), input); !strings.Contains(err.Error(), "unsupported current book") {
		t.Fatalf("authorization writer error = %v, want current-book rejection", err)
	}
}

func TestReservedColumnMigrationUsesSQLiteWriterExclusionAndRollback(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("20260831000000_billing_remove_reserved_nano.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, required := range []string{"db.DB.Conn", "BEGIN IMMEDIATE", "ROLLBACK", "billing_accounts_phase4", "billing_reconciliation_events_phase4", "BeginTx", "LOCK TABLE billing_accounts IN ACCESS EXCLUSIVE MODE", "DROP COLUMN IF EXISTS reserved_nano"} {
		if !strings.Contains(source, required) {
			t.Fatalf("reserved-column migration missing safety primitive %q", required)
		}
	}
}
