package billing

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func testAccount() Account {
	return Account{ID: "acct-1", Currency: "USD", Mode: AccountPrepaid, State: AccountReady, BalanceNano: 100, Version: 1}
}

func TestMoneyCheckedArithmeticAndCurrency(t *testing.T) {
	t.Parallel()
	usd := Money{Nano: 7, Currency: "USD"}
	got, err := usd.Add(Money{Nano: 5, Currency: "USD"})
	if err != nil || got.Nano != 12 {
		t.Fatalf("Add = %#v, %v; want 12 USD", got, err)
	}
	if _, err := usd.Add(Money{Nano: 1, Currency: "EUR"}); !errors.Is(err, ErrMoneyCurrencyMismatch) {
		t.Fatalf("currency mismatch = %v, want ErrMoneyCurrencyMismatch", err)
	}
	if _, err := (Money{Nano: math.MaxInt64, Currency: "USD"}).Add(usd); !errors.Is(err, ErrMoneyOverflow) {
		t.Fatalf("overflow = %v, want ErrMoneyOverflow", err)
	}
	if _, err := (Money{Nano: math.MinInt64, Currency: "USD"}).Neg(); !errors.Is(err, ErrMoneyOverflow) {
		t.Fatalf("negation overflow = %v, want ErrMoneyOverflow", err)
	}
	if _, err := (Money{Nano: 1, Currency: "USD"}).Sub(Money{Nano: math.MinInt64, Currency: "USD"}); !errors.Is(err, ErrMoneyOverflow) {
		t.Fatalf("sub MinInt64 = %v, want ErrMoneyOverflow", err)
	}
	if _, err := NewMoney(1, ""); !errors.Is(err, ErrMoneyInvalid) {
		t.Fatalf("empty currency = %v, want ErrMoneyInvalid", err)
	}
	if _, err := NewMoney(1, "   "); !errors.Is(err, ErrMoneyInvalid) {
		t.Fatalf("blank currency = %v, want ErrMoneyInvalid", err)
	}
}

func TestAccountSpendableUsesModeFloorAndReserved(t *testing.T) {
	t.Parallel()
	prepaid := testAccount()
	prepaid.ReservedNano = 30
	if got, err := prepaid.SpendableNano(); err != nil || got != 70 {
		t.Fatalf("prepaid spendable = %d, %v; want 70", got, err)
	}
	postpaid := Account{ID: "acct-2", Currency: "USD", Mode: AccountPostpaid, CreditLimit: 100, BalanceNano: -35, ReservedNano: 10, State: AccountReady}
	if got, err := postpaid.SpendableNano(); err != nil || got != 55 {
		t.Fatalf("postpaid spendable = %d, %v; want 55", got, err)
	}
}

func TestAccountAuthorizeIsImmutableAndCannotCrossFloor(t *testing.T) {
	t.Parallel()
	original := testAccount()
	updated, err := original.Authorize(Money{Nano: 60, Currency: "USD"})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if original.ReservedNano != 0 || updated.ReservedNano != 60 {
		t.Fatalf("authorization mutated receiver: original=%d updated=%d", original.ReservedNano, updated.ReservedNano)
	}
	if _, err := original.Authorize(Money{Nano: 101, Currency: "USD"}); !errors.Is(err, ErrInsufficientSpendable) {
		t.Fatalf("overspend = %v, want ErrInsufficientSpendable", err)
	}
	blocked := original
	blocked.State = AccountReconcileRequired
	if _, err := blocked.Authorize(Money{Nano: 1, Currency: "USD"}); !errors.Is(err, ErrAccountNotReady) {
		t.Fatalf("blocked authorization = %v, want ErrAccountNotReady", err)
	}
}

func TestJournalTransactionRequiresBalancedPositiveEntries(t *testing.T) {
	t.Parallel()
	base := JournalTransaction{
		ID: "tx-1", Book: JournalBookFinancial, Currency: "USD", SourceKey: "source-1",
		Entries: []JournalEntry{
			{LedgerAccount: "customer", Side: JournalDebit, Amount: Money{Nano: 10, Currency: "USD"}},
			{LedgerAccount: "revenue", Side: JournalCredit, Amount: Money{Nano: 10, Currency: "USD"}},
		},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("balanced journal validation: %v", err)
	}
	base.Entries[1].Amount.Nano = 9
	if err := base.Validate(); !errors.Is(err, ErrJournalUnbalanced) {
		t.Fatalf("unbalanced validation = %v, want ErrJournalUnbalanced", err)
	}
	base.Entries[1].Amount.Nano = 10
	base.Entries[0].Amount.Nano = 0
	if err := base.Validate(); !errors.Is(err, ErrJournalInvalid) {
		t.Fatalf("zero posting validation = %v, want ErrJournalInvalid", err)
	}
}

func TestJournalDetachedCopiesEntries(t *testing.T) {
	t.Parallel()
	j := JournalTransaction{
		ID: "tx-1", Book: JournalBookAuthorization, Currency: "USD", SourceKey: "source-1",
		Entries: []JournalEntry{
			{LedgerAccount: "reserved", Side: JournalDebit, Amount: Money{Nano: 3, Currency: "USD"}},
			{LedgerAccount: "contra", Side: JournalCredit, Amount: Money{Nano: 3, Currency: "USD"}},
		},
	}
	copy, err := j.Detached()
	if err != nil {
		t.Fatalf("Detached: %v", err)
	}
	j.Entries[0].Amount.Nano = 99
	if copy.Entries[0].Amount.Nano != 3 {
		t.Fatalf("detached journal shares mutable entry storage: %#v", copy)
	}
}

func TestJournalCanonicalFingerprintIsVersioned(t *testing.T) {
	t.Parallel()
	j := JournalTransaction{
		ID: "tx-fp", Book: JournalBookFinancial, Currency: "USD", SourceKey: "source-fp",
		AccountID: "acct-fp", AccountSequence: 99,
		Entries: []JournalEntry{
			{LedgerAccount: "customer", Side: JournalDebit, Amount: Money{Nano: 5, Currency: "USD"}},
			{LedgerAccount: "revenue", Side: JournalCredit, Amount: Money{Nano: 5, Currency: "USD"}},
		},
	}
	fp, err := j.CanonicalFingerprint()
	if err != nil {
		t.Fatalf("CanonicalFingerprint: %v", err)
	}
	const wantPrefix = "journal-fp:v2:"
	if JournalFingerprintPrefix != wantPrefix {
		t.Fatalf("JournalFingerprintPrefix = %q, want %q", JournalFingerprintPrefix, wantPrefix)
	}
	if !strings.HasPrefix(fp, wantPrefix) {
		t.Fatalf("fingerprint %q missing version prefix %q", fp, wantPrefix)
	}
	sealed, err := j.Seal()
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed.SemanticFingerprint != fp {
		t.Fatalf("Seal fingerprint = %q, want %q", sealed.SemanticFingerprint, fp)
	}
	// DB identity / allocated sequence / stored fingerprint / wall-clock fields
	// and point-in-time snapshot diagnostics must not change semantic meaning.
	j.ID = "other-id"
	j.AccountSequence = 1
	j.SemanticFingerprint = "ignored"
	j.BalanceBefore = 100
	j.BalanceAfter = 90
	j.ReservedBefore = 5
	j.ReservedAfter = 0
	j.SpendableBefore = 95
	j.SpendableAfter = 90
	j.CreditFloor = -10
	j.CreditLimit = 10
	j.Mode = "postpaid"
	j.SnapshotVersionBefore = 3
	j.SnapshotVersionAfter = 4
	again, err := j.CanonicalFingerprint()
	if err != nil || again != fp {
		t.Fatalf("identity/sequence/snapshots must be excluded: got %q want %q err=%v", again, fp, err)
	}
}

func TestApplyBalanceDeltaEnforcesCreditFloorAndImmutability(t *testing.T) {
	t.Parallel()
	prepaid := testAccount()
	updated, err := prepaid.ApplyBalanceDelta(Money{Nano: -40, Currency: "USD"})
	if err != nil || updated.BalanceNano != 60 || prepaid.BalanceNano != 100 {
		t.Fatalf("legal prepaid delta = %#v err=%v; original=%#v", updated, err, prepaid)
	}
	if _, err := prepaid.ApplyBalanceDelta(Money{Nano: -101, Currency: "USD"}); !errors.Is(err, ErrInsufficientSpendable) {
		t.Fatalf("prepaid below floor = %v, want ErrInsufficientSpendable", err)
	}
	postpaid := Account{ID: "acct-2", Currency: "USD", Mode: AccountPostpaid, CreditLimit: 50, BalanceNano: -10, State: AccountReady}
	ok, err := postpaid.ApplyBalanceDelta(Money{Nano: -40, Currency: "USD"})
	if err != nil || ok.BalanceNano != -50 || postpaid.BalanceNano != -10 {
		t.Fatalf("legal postpaid delta = %#v err=%v; original=%#v", ok, err, postpaid)
	}
	if _, err := postpaid.ApplyBalanceDelta(Money{Nano: -41, Currency: "USD"}); !errors.Is(err, ErrInsufficientSpendable) {
		t.Fatalf("postpaid below floor = %v, want ErrInsufficientSpendable", err)
	}
}
