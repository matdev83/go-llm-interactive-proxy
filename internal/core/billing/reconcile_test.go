package billing

import "testing"

func reconcileJournal(id string, book JournalBook, sequence uint64, debitAccount string, debitSide JournalSide, debit int64, creditAccount string, creditSide JournalSide, credit int64) JournalTransaction {
	journal := JournalTransaction{ID: id, Book: book, Currency: "USD", SourceKey: id, AccountID: "acct", AccountSequence: sequence, Entries: []JournalEntry{
		{LedgerAccount: debitAccount, Side: debitSide, Amount: Money{Nano: debit, Currency: "USD"}},
		{LedgerAccount: creditAccount, Side: creditSide, Amount: Money{Nano: credit, Currency: "USD"}},
	}}
	sealed, err := journal.Seal()
	if err != nil {
		panic(err)
	}
	return sealed
}

func TestReplayAccountRebuildsFinancialBook(t *testing.T) {
	t.Parallel()
	account := Account{ID: "acct", Currency: "USD", Mode: AccountPrepaid, State: AccountReady, Version: 4}
	journal := reconcileJournal("charge", JournalBookFinancial, 1, "customer_financial_account", JournalDebit, 7, "usage_revenue", JournalCredit, 7)
	report := ReplayAccount(account, 100, []JournalTransaction{journal})
	if !report.OK || report.Rebuilt.BalanceNano != 93 || report.Rebuilt.SpendableNano != 93 {
		t.Fatalf("replay report = %+v, want balance 93 spendable 93", report)
	}
}

func TestReplayAccountReportsSequenceAndFingerprintCorruption(t *testing.T) {
	t.Parallel()
	account := Account{ID: "acct", Currency: "USD", Mode: AccountPrepaid, State: AccountReady, Version: 2}
	journal := reconcileJournal("charge", JournalBookFinancial, 2, "customer_financial_account", JournalDebit, 7, "usage_revenue", JournalCredit, 7)
	journal.SemanticFingerprint = "tampered"
	report := ReplayAccount(account, 100, []JournalTransaction{journal})
	if report.OK || len(report.Issues) == 0 || report.FirstMismatchSequence != 2 {
		t.Fatalf("corruption report = %+v", report)
	}
	if report.Rebuilt.BalanceNano != 100 {
		t.Fatalf("tampered journal must not mutate rebuilt balances: %+v", report.Rebuilt)
	}
}

func TestReplayAccountReportsReplacementWithoutPriorReversal(t *testing.T) {
	t.Parallel()
	account := Account{ID: "acct", Currency: "USD", Mode: AccountPrepaid, State: AccountReady, Version: 2}
	original := reconcileJournal("tx-original", JournalBookFinancial, 1, "customer_financial_account", JournalDebit, 10, "usage_revenue", JournalCredit, 10)
	replacement := reconcileJournal("tx-replacement", JournalBookFinancial, 2, "customer_financial_account", JournalDebit, 9, "usage_revenue", JournalCredit, 9)
	replacement.CorrectsTransactionID = original.ID
	replacement.CorrectionGroupID = original.ID
	sealed, err := replacement.Seal()
	if err != nil {
		t.Fatal(err)
	}
	report := ReplayAccount(account, 100, []JournalTransaction{original, sealed})
	if report.OK {
		t.Fatalf("expected replacement_without_reversal, got OK report=%+v", report)
	}
	found := false
	for _, issue := range report.Issues {
		if issue.Code == "replacement_without_reversal" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("issues = %+v, want replacement_without_reversal", report.Issues)
	}
}
