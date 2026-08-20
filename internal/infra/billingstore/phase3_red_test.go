package billingstore

import (
	"context"
	"database/sql"
	"sort"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestPhase3ProviderJournalUsesNullAccountSequence(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "phase3-null-seq", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-null-seq")
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{
		AccountID: account.ID, CallID: callID, Leg: leg,
		Result: billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 7, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true},
	})
	if err != nil {
		t.Fatalf("provider COGS post: %v", err)
	}
	var sequence sql.NullInt64
	if err := store.db.NewRaw(`SELECT account_sequence FROM journal_transactions WHERE operation_kind = 'provider_call_cogs' AND source_key = ?`, func() string { key, _ := billing.ProviderCostSourceKey(sealed.Key); return key }()).Scan(ctx, &sequence); err != nil {
		t.Fatal(err)
	}
	if sequence.Valid {
		t.Fatalf("provider account_sequence = %d, want SQL NULL", sequence.Int64)
	}
}

func TestPhase3ProviderOrderUsesRecordedAtAndTransactionID(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "phase3-provider-order", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	type item struct {
		leg    billing.CallLegUsageRecord
		sealed billing.CallLegUsageRecord
		key    string
	}
	items := make([]item, 0, 2)
	for _, bLegID := range []string{"b-order-a", "b-order-b"} {
		callID, err := billing.NewBillingCallID()
		if err != nil {
			t.Fatal(err)
		}
		leg := testIndependentCallLegFor(callID, bLegID)
		sealed, err := leg.Seal()
		if err != nil {
			t.Fatal(err)
		}
		key, err := billing.ProviderCostSourceKey(sealed.Key)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, item{leg: leg, sealed: sealed, key: key})
	}
	// Deliberately post the lexically later transaction first. Equal recorded_at
	// values must still be ordered by the immutable transaction-id tie-breaker.
	sort.Slice(items, func(i, j int) bool { return items[i].key > items[j].key })
	for _, item := range items {
		if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{
			AccountID: account.ID, CallID: item.leg.CallID, Leg: item.leg,
			Result: billing.OperatorCostResult{LURKey: item.sealed.Key, Amount: billing.Money{Nano: 3, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true},
		}); err != nil {
			t.Fatal(err)
		}
	}
	journals, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(items))
	for _, journal := range journals {
		if journal.OperationKind == "provider_call_cogs" {
			got = append(got, journal.ID)
			if journal.RecordedAt.IsZero() {
				t.Fatal("provider journal did not retain recorded_at")
			}
		}
	}
	byID := make(map[string]time.Time, len(journals))
	for _, journal := range journals {
		byID[journal.ID] = journal.RecordedAt
	}
	want := append([]string(nil), got...)
	sort.Slice(want, func(i, j int) bool {
		if byID[want[i]].Equal(byID[want[j]]) {
			return want[i] < want[j]
		}
		return byID[want[i]].Before(byID[want[j]])
	})
	if len(got) != len(want) {
		t.Fatalf("provider order = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("provider order = %v, want %v", got, want)
		}
	}
}

func TestPhase3ReplayAccountRejectsZeroSequenceCustomerJournal(t *testing.T) {
	t.Parallel()
	account := billing.Account{ID: "phase3-replay-zero", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 90, State: billing.AccountReady, Version: 2}
	customer := billing.JournalTransaction{
		ID: "customer-zero-seq", Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: "customer-zero-seq", AccountID: account.ID,
		OperationKind: "customer_settlement",
		Entries:       []billing.JournalEntry{{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 10, Currency: "USD"}}, {LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: 10, Currency: "USD"}}},
	}
	sealed, err := customer.Seal()
	if err != nil {
		t.Fatal(err)
	}
	report := billing.ReplayAccount(account, 100, []billing.JournalTransaction{sealed})
	if report.OK {
		t.Fatalf("zero-sequence customer journal replay = OK, want fail closed: %+v", report)
	}
	for _, issue := range report.Issues {
		if issue.Code == "account_sequence_invalid" {
			return
		}
	}
	t.Fatalf("issues = %+v, want account_sequence_invalid", report.Issues)
}

func TestPhase3ReplayAccountIgnoresProviderSequence(t *testing.T) {
	t.Parallel()
	account := billing.Account{ID: "phase3-replay", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 90, State: billing.AccountReady, Version: 2}
	customer := billing.JournalTransaction{
		ID: "customer-seq-1", Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: "customer-seq-1", AccountID: account.ID,
		AccountSequence: 1, OperationKind: "customer_settlement",
		Entries: []billing.JournalEntry{{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 10, Currency: "USD"}}, {LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: 10, Currency: "USD"}}},
	}
	provider := billing.JournalTransaction{
		ID: "provider-cogs-1", Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: "provider-cogs-1", AccountID: account.ID,
		OperationKind: "provider_call_cogs", TurnID: "call-1", BLegID: "b-1",
		Entries: []billing.JournalEntry{{LedgerAccount: "inference_provider_cogs", Side: billing.JournalDebit, Amount: billing.Money{Nano: 2, Currency: "USD"}}, {LedgerAccount: "provider_payable_clearing", Side: billing.JournalCredit, Amount: billing.Money{Nano: 2, Currency: "USD"}}},
	}
	var err error
	customer, err = customer.Seal()
	if err != nil {
		t.Fatal(err)
	}
	provider, err = provider.Seal()
	if err != nil {
		t.Fatal(err)
	}
	report := billing.ReplayAccount(account, 100, []billing.JournalTransaction{provider, customer})
	if !report.OK {
		t.Fatalf("provider row made customer replay fail: %+v", report.Issues)
	}
	if report.Rebuilt.BalanceNano != 90 {
		t.Fatalf("rebuilt balance = %d, want 90", report.Rebuilt.BalanceNano)
	}
}

func TestPhase3JournalSchemaAllowsNullProviderSequence(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	var notNull int
	if err := store.db.NewRaw(`SELECT "notnull" FROM pragma_table_info('journal_transactions') WHERE name = 'account_sequence'`).Scan(ctx, &notNull); err != nil {
		t.Fatal(err)
	}
	if notNull != 0 {
		t.Fatalf("journal account_sequence notnull = %d, want nullable", notNull)
	}
}

func TestPhase3GenericJournalWriterRejectsProviderCOGS(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "phase3-generic-provider", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	input := billing.JournalTransaction{
		ID: "generic-provider", Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: "generic-provider", AccountID: account.ID,
		OperationKind: "provider_call_cogs",
		Entries:       []billing.JournalEntry{{LedgerAccount: "inference_provider_cogs", Side: billing.JournalDebit, Amount: billing.Money{Nano: 1, Currency: "USD"}}, {LedgerAccount: "provider_payable_clearing", Side: billing.JournalCredit, Amount: billing.Money{Nano: 1, Currency: "USD"}}},
	}
	if _, err := store.postJournalTransaction(ctx, input); err == nil {
		t.Fatal("generic journal writer accepted provider COGS")
	}
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM journal_transactions WHERE source_key = ?`, input.SourceKey).Scan(ctx, &count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("generic provider rows = %d, want 0", count)
	}
}

func TestPhase3ProviderRangeOrdersHistoricalAndNewByRecordedAt(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "phase3-provider-range", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	insert := func(id string, sequence any, recorded string) {
		t.Helper()
		if _, err := store.db.NewRaw(`INSERT INTO journal_transactions(transaction_id, account_id, book, currency, source_key, semantic_fingerprint, turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id, correction_group_id, operation_kind, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, account.ID, "financial", "USD", id, "legacy", "", "", "", sequence, "", "", "", "provider_call_cogs", 100, 100, 0, 0, 100, 100, 0, 0, "prepaid", 1, 1, recorded).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	insert("provider-historical", int64(77), "2021-01-01T00:00:00Z")
	insert("provider-new", nil, "2020-01-01T00:00:00Z")
	rows, err := store.loadJournalRange(ctx, account.ID, account.Currency, billing.JournalBookFinancial, "provider_call_cogs", time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "provider-new" || rows[1].ID != "provider-historical" {
		t.Fatalf("provider range order = %#v, want new then historical", rows)
	}
}

func TestPhase3JournalMigrationRunsThroughRunner(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "phase3-history", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	const recorded = "2020-01-02T03:04:05Z"
	if _, err := store.db.NewRaw(`INSERT INTO journal_transactions(transaction_id, account_id, book, currency, source_key, semantic_fingerprint, turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id, correction_group_id, operation_kind, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"historical-provider", account.ID, "financial", "USD", "historical-provider", "legacy-fingerprint", "call-history", "a-history", "b-history", 77, "", "", "", "provider_call_cogs", 100, 100, 0, 0, 100, 100, 0, 0, "prepaid", 1, 1, recorded).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`DELETE FROM bun_billing_migrations WHERE name IN (?, ?)`, ProviderJournalOrderMigrationName, ProviderJournalSequenceContractMigrationName).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, store.db); err != nil {
		t.Fatalf("migration runner: %v", err)
	}
	var sequence sql.NullInt64
	var gotRecorded string
	if err := store.db.NewRaw(`SELECT account_sequence, recorded_at FROM journal_transactions WHERE transaction_id = ?`, "historical-provider").Scan(ctx, &sequence, &gotRecorded); err != nil {
		t.Fatal(err)
	}
	if !sequence.Valid || sequence.Int64 != 77 || gotRecorded != recorded {
		t.Fatalf("historical provider row changed: sequence=%+v recorded_at=%q", sequence, gotRecorded)
	}
	if err := VerifySchema(ctx, store.db); err != nil {
		t.Fatalf("VerifySchema after journal migration: %v", err)
	}
}
