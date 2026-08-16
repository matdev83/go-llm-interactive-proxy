package billing

import (
	"fmt"
	"strconv"
	"testing"
)

func TestReplayAccountRebuildsFinancialBookAndZerosReserved(t *testing.T) {
	t.Parallel()
	account := Account{ID: "acct", Currency: "USD", Mode: AccountPrepaid, State: AccountReady, Version: 4}
	journals := []JournalTransaction{
		reconcileJournal("auth", JournalBookLegacyAuthorization, 1, "customer_reserved_exposure", JournalDebit, 20, "authorization_contra", JournalCredit, 20),
		reconcileJournal("charge", JournalBookFinancial, 2, "customer_financial_account", JournalDebit, 7, "usage_revenue", JournalCredit, 7),
		reconcileJournal("release", JournalBookLegacyAuthorization, 3, "authorization_contra", JournalDebit, 13, "customer_reserved_exposure", JournalCredit, 13),
	}
	report := ReplayAccount(account, 100, journals)
	if !report.OK || report.Rebuilt.BalanceNano != 93 || report.Rebuilt.ReservedNano != 0 || report.Rebuilt.SpendableNano != 93 {
		t.Fatalf("replay report = %+v, want balance 93 reserved 0 spendable 93", report)
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
	if report.Rebuilt.BalanceNano != 100 || report.Rebuilt.ReservedNano != 0 {
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

func TestReplayAccountMatchesDeterministicOperationFixtures(t *testing.T) {
	t.Parallel()
	fixtures := []replayFixture{
		{name: "prepaid fund authorize settle", mode: AccountPrepaid, opening: 100, ops: []replayOp{
			{kind: replayFund, nano: 40},
			{kind: replayAuthorize, nano: 30},
			{kind: replaySettle, nano: 12},
		}},
		{name: "prepaid unused hold release", mode: AccountPrepaid, opening: 50, ops: []replayOp{
			{kind: replayAuthorize, nano: 20},
			{kind: replayRelease, nano: 0},
			{kind: replayFund, nano: 15},
		}},
		{name: "prepaid reverse replace funding", mode: AccountPrepaid, opening: 80, ops: []replayOp{
			{kind: replayFund, nano: 25},
			{kind: replayReverseReplace, nano: 10},
			{kind: replayAuthorize, nano: 5},
			{kind: replaySettle, nano: 5},
		}},
		{name: "postpaid fund charge stays above floor", mode: AccountPostpaid, opening: 0, creditLimit: 50, ops: []replayOp{
			{kind: replayFund, nano: 10},
			{kind: replayAuthorize, nano: 40},
			{kind: replaySettle, nano: 30},
		}},
		{name: "postpaid authorize release then settle sibling", mode: AccountPostpaid, opening: -10, creditLimit: 100, ops: []replayOp{
			{kind: replayAuthorize, nano: 20},
			{kind: replayRelease, nano: 0},
			{kind: replayAuthorize, nano: 15},
			{kind: replaySettle, nano: 8},
		}},
		{name: "repeated fund authorize settle", mode: AccountPrepaid, opening: 200, ops: []replayOp{
			{kind: replayFund, nano: 5},
			{kind: replayAuthorize, nano: 9},
			{kind: replaySettle, nano: 3},
			{kind: replayFund, nano: 7},
			{kind: replayAuthorize, nano: 4},
			{kind: replaySettle, nano: 4},
			{kind: replayFund, nano: 1},
		}},
		{name: "zero remaining settle closes hold", mode: AccountPrepaid, opening: 60, ops: []replayOp{
			{kind: replayAuthorize, nano: 11},
			{kind: replaySettle, nano: 11},
		}},
		{name: "replace then authorize from net funding", mode: AccountPrepaid, opening: 30, ops: []replayOp{
			{kind: replayFund, nano: 20},
			{kind: replayReverseReplace, nano: 8},
			{kind: replayAuthorize, nano: 8},
			{kind: replayRelease, nano: 0},
		}},
	}
	fixtures = append(fixtures, generateReplayFixtures()...)
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			runReplayFixture(t, fixture)
		})
	}
}

type replayOpKind string

const (
	replayFund           replayOpKind = "fund"
	replayAuthorize      replayOpKind = "authorize"
	replaySettle         replayOpKind = "settle"
	replayRelease        replayOpKind = "release"
	replayReverseReplace replayOpKind = "reverse_replace"
)

type replayOp struct {
	kind replayOpKind
	nano int64
}

type replayFixture struct {
	name        string
	mode        AccountMode
	opening     int64
	creditLimit int64
	ops         []replayOp
}

func runReplayFixture(t *testing.T, fixture replayFixture) {
	t.Helper()
	account := Account{ID: "acct", Currency: "USD", Mode: fixture.mode, CreditLimit: fixture.creditLimit, BalanceNano: fixture.opening, State: AccountReady, Version: 1}
	var journals []JournalTransaction
	var lastFinancial JournalTransaction
	var openHold int64
	seq := uint64(0)
	appendJournal := func(journal JournalTransaction) JournalTransaction {
		seq++
		journal.AccountSequence = seq
		sealed, err := journal.Seal()
		if err != nil {
			t.Fatal(err)
		}
		journals = append(journals, sealed)
		return sealed
	}
	for i, op := range fixture.ops {
		switch op.kind {
		case replayFund:
			lastFinancial = appendJournal(JournalTransaction{
				ID: "funding-" + strconv.Itoa(i), Book: JournalBookFinancial, Currency: "USD", SourceKey: "funding-" + strconv.Itoa(i), AccountID: "acct",
				Entries: []JournalEntry{
					{LedgerAccount: "cash_payment_clearing", Side: JournalDebit, Amount: Money{Nano: op.nano, Currency: "USD"}},
					{LedgerAccount: "customer_financial_account", Side: JournalCredit, Amount: Money{Nano: op.nano, Currency: "USD"}},
				},
			})
			account.BalanceNano += op.nano
		case replayAuthorize:
			if openHold != 0 {
				t.Fatalf("op %d authorize while hold %d is open", i, openHold)
			}
			appendJournal(JournalTransaction{
				ID: "auth-" + strconv.Itoa(i), Book: JournalBookLegacyAuthorization, Currency: "USD", SourceKey: "auth-" + strconv.Itoa(i), AccountID: "acct",
				Entries: []JournalEntry{
					{LedgerAccount: "customer_reserved_exposure", Side: JournalDebit, Amount: Money{Nano: op.nano, Currency: "USD"}},
					{LedgerAccount: "authorization_contra", Side: JournalCredit, Amount: Money{Nano: op.nano, Currency: "USD"}},
				},
			})
			openHold = op.nano
		case replaySettle:
			if openHold < op.nano {
				t.Fatalf("op %d settle %d exceeds hold %d", i, op.nano, openHold)
			}
			appendJournal(JournalTransaction{
				ID: "settle-" + strconv.Itoa(i), Book: JournalBookFinancial, Currency: "USD", SourceKey: "settle-" + strconv.Itoa(i), AccountID: "acct",
				Entries: []JournalEntry{
					{LedgerAccount: "customer_financial_account", Side: JournalDebit, Amount: Money{Nano: op.nano, Currency: "USD"}},
					{LedgerAccount: "usage_revenue", Side: JournalCredit, Amount: Money{Nano: op.nano, Currency: "USD"}},
				},
			})
			account.BalanceNano -= op.nano
			appendJournal(JournalTransaction{
				ID: "release-" + strconv.Itoa(i), Book: JournalBookLegacyAuthorization, Currency: "USD", SourceKey: "release-" + strconv.Itoa(i), AccountID: "acct",
				Entries: []JournalEntry{
					{LedgerAccount: "authorization_contra", Side: JournalDebit, Amount: Money{Nano: openHold, Currency: "USD"}},
					{LedgerAccount: "customer_reserved_exposure", Side: JournalCredit, Amount: Money{Nano: openHold, Currency: "USD"}},
				},
			})
			openHold = 0
		case replayRelease:
			if openHold == 0 {
				t.Fatalf("op %d release with no hold", i)
			}
			appendJournal(JournalTransaction{
				ID: "release-" + strconv.Itoa(i), Book: JournalBookLegacyAuthorization, Currency: "USD", SourceKey: "release-" + strconv.Itoa(i), AccountID: "acct",
				Entries: []JournalEntry{
					{LedgerAccount: "authorization_contra", Side: JournalDebit, Amount: Money{Nano: openHold, Currency: "USD"}},
					{LedgerAccount: "customer_reserved_exposure", Side: JournalCredit, Amount: Money{Nano: openHold, Currency: "USD"}},
				},
			})
			openHold = 0
		case replayReverseReplace:
			if lastFinancial.ID == "" {
				t.Fatalf("op %d reverse_replace without prior funding", i)
			}
			reversal := lastFinancial
			reversal.ID = lastFinancial.ID + "-rev"
			reversal.SourceKey = reversal.ID
			reversal.AccountSequence = 0
			reversal.SemanticFingerprint = ""
			reversal.ReversalOf = lastFinancial.ID
			reversal.CorrectionGroupID = lastFinancial.ID
			reversal.Entries = []JournalEntry{
				{LedgerAccount: lastFinancial.Entries[1].LedgerAccount, Side: JournalDebit, Amount: lastFinancial.Entries[1].Amount},
				{LedgerAccount: lastFinancial.Entries[0].LedgerAccount, Side: JournalCredit, Amount: lastFinancial.Entries[0].Amount},
			}
			appendJournal(reversal)
			account.BalanceNano -= lastFinancial.Entries[1].Amount.Nano
			replacement := lastFinancial
			replacement.ID = lastFinancial.ID + "-rep"
			replacement.SourceKey = replacement.ID
			replacement.AccountSequence = 0
			replacement.SemanticFingerprint = ""
			replacement.ReversalOf = ""
			replacement.CorrectsTransactionID = lastFinancial.ID
			replacement.CorrectionGroupID = lastFinancial.ID
			replacement.Entries = []JournalEntry{
				{LedgerAccount: "cash_payment_clearing", Side: JournalDebit, Amount: Money{Nano: op.nano, Currency: "USD"}},
				{LedgerAccount: "customer_financial_account", Side: JournalCredit, Amount: Money{Nano: op.nano, Currency: "USD"}},
			}
			lastFinancial = appendJournal(replacement)
			account.BalanceNano += op.nano
		default:
			t.Fatalf("unknown op %q", op.kind)
		}
		account.Version++
		floor := account.CreditFloorNano()
		if account.Mode == AccountPrepaid && account.BalanceNano < 0 {
			t.Fatalf("after op %d prepaid balance %d < 0", i, account.BalanceNano)
		}
		if account.Mode == AccountPostpaid && account.BalanceNano < -account.CreditLimit {
			t.Fatalf("after op %d postpaid balance %d < -creditLimit %d", i, account.BalanceNano, account.CreditLimit)
		}
		report := ReplayAccount(account, fixture.opening, journals)
		if !report.OK {
			t.Fatalf("after op %d replay issues = %+v", i, report.Issues)
		}
		spendable, err := account.SpendableNano()
		if err != nil {
			t.Fatal(err)
		}
		if report.Rebuilt.BalanceNano != account.BalanceNano || report.Rebuilt.ReservedNano != 0 || report.Rebuilt.SpendableNano != spendable {
			t.Fatalf("after op %d rebuilt=%+v account balance=%d spendable=%d (reserved must rebuild as 0)", i, report.Rebuilt, account.BalanceNano, spendable)
		}
		if account.BalanceNano < floor {
			t.Fatalf("after op %d balance %d below floor %d", i, account.BalanceNano, floor)
		}
	}
}

func generateReplayFixtures() []replayFixture {
	funds := []int64{5, 12, 25}
	holds := []int64{4, 9, 15}
	charges := []int64{1, 4, 9}
	out := make([]replayFixture, 0, 24)
	for _, fund := range funds {
		for _, hold := range holds {
			for _, charge := range charges {
				if charge > hold {
					continue
				}
				out = append(out, replayFixture{
					name:    fmt.Sprintf("prepaid fund=%d hold=%d charge=%d", fund, hold, charge),
					mode:    AccountPrepaid,
					opening: 80,
					ops: []replayOp{
						{kind: replayFund, nano: fund},
						{kind: replayAuthorize, nano: hold},
						{kind: replaySettle, nano: charge},
					},
				})
			}
		}
	}
	out = append(out,
		replayFixture{name: "postpaid generated authorize release fund", mode: AccountPostpaid, opening: 0, creditLimit: 80, ops: []replayOp{
			{kind: replayAuthorize, nano: 20},
			{kind: replayRelease, nano: 0},
			{kind: replayFund, nano: 15},
		}},
		replayFixture{name: "postpaid generated fund reverse replace settle", mode: AccountPostpaid, opening: -5, creditLimit: 60, ops: []replayOp{
			{kind: replayFund, nano: 20},
			{kind: replayReverseReplace, nano: 12},
			{kind: replayAuthorize, nano: 10},
			{kind: replaySettle, nano: 6},
		}},
		replayFixture{name: "prepaid generated unused then sibling settle", mode: AccountPrepaid, opening: 40, ops: []replayOp{
			{kind: replayAuthorize, nano: 11},
			{kind: replayRelease, nano: 0},
			{kind: replayAuthorize, nano: 8},
			{kind: replaySettle, nano: 8},
		}},
	)
	return out
}

func reconcileJournal(id string, book JournalBook, sequence uint64, debitAccount string, debitSide JournalSide, debitAmount int64, creditAccount string, creditSide JournalSide, creditAmount int64) JournalTransaction {
	journal := JournalTransaction{
		ID: id, Book: book, Currency: "USD", SourceKey: id, AccountID: "acct", AccountSequence: sequence,
		Entries: []JournalEntry{
			{LedgerAccount: debitAccount, Side: debitSide, Amount: Money{Nano: debitAmount, Currency: "USD"}},
			{LedgerAccount: creditAccount, Side: creditSide, Amount: Money{Nano: creditAmount, Currency: "USD"}},
		},
	}
	sealed, _ := journal.Seal()
	return sealed
}
