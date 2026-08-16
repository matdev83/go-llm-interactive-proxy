package billingstore

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteTrustedFundingPaymentAdjustmentAndPolicyAreAtomicAndReplaySafe(t *testing.T) {
	runTrustedFundingPaymentAdjustment(t, newSQLiteTestStore(t), "trusted-account")
}

func TestSQLiteTrustedOperationsAreAccountScoped(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	for _, accountID := range []string{"acct-a", "acct-b"} {
		if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 10, State: billing.AccountReady, Version: 1}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.PostFunding(ctx, billing.FundingInput{AccountID: "acct-a", Amount: billing.Money{Nano: 5, Currency: "USD"}, SourceKey: "shared-bank", Reason: "topup"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PostFunding(ctx, billing.FundingInput{AccountID: "acct-b", Amount: billing.Money{Nano: 7, Currency: "USD"}, SourceKey: "shared-bank", Reason: "topup"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Transaction.ID == second.Transaction.ID {
		t.Fatalf("journal IDs collided across accounts: %q", first.Transaction.ID)
	}
	if first.OperationKey == second.OperationKey {
		t.Fatalf("operation keys collided across accounts: %q", first.OperationKey)
	}
	acctA, err := store.GetAccount(ctx, "acct-a")
	if err != nil || acctA.BalanceNano != 15 {
		t.Fatalf("acct-a = %+v, %v", acctA, err)
	}
	acctB, err := store.GetAccount(ctx, "acct-b")
	if err != nil || acctB.BalanceNano != 17 {
		t.Fatalf("acct-b = %+v, %v", acctB, err)
	}
}

func TestSQLiteTrustedOperationsDistinguishColonAmbiguousAccountSourcePairs(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	for _, accountID := range []string{"a:b", "a"} {
		if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 10, State: billing.AccountReady, Version: 1}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.PostFunding(ctx, billing.FundingInput{AccountID: "a:b", Amount: billing.Money{Nano: 5, Currency: "USD"}, SourceKey: "c", Reason: "topup"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PostFunding(ctx, billing.FundingInput{AccountID: "a", Amount: billing.Money{Nano: 7, Currency: "USD"}, SourceKey: "b:c", Reason: "topup"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Transaction.ID == second.Transaction.ID {
		t.Fatalf("colon-ambiguous journal IDs collided: %q", first.Transaction.ID)
	}
	if first.OperationKey == second.OperationKey {
		t.Fatalf("colon-ambiguous operation keys collided: %q", first.OperationKey)
	}
}

func TestSQLiteTrustedDebitCannotCrossPostpaidFloor(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "floor-account", Currency: "USD", Mode: billing.AccountPostpaid, CreditLimit: 10, BalanceNano: -9, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	_, err := store.PostAdjustment(ctx, billing.AdjustmentInput{AccountID: "floor-account", Amount: billing.Money{Nano: 2, Currency: "USD"}, Direction: billing.AdjustmentDebit, SourceKey: "too-low", Reason: "bad"})
	if !errors.Is(err, billing.ErrInsufficientSpendable) {
		t.Fatalf("floor debit = %v", err)
	}
	if account, err := store.GetAccount(ctx, "floor-account"); err != nil || account.BalanceNano != -9 || account.Version != 1 {
		t.Fatalf("floor account mutated = %+v, %v", account, err)
	}
}

func TestSQLiteCreditPolicyRejectsUnsafeReductionAndReplaysByFingerprint(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "policy-account", Currency: "USD", Mode: billing.AccountPostpaid, CreditLimit: 100, BalanceNano: -50, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	_, err := store.ChangeCreditPolicy(ctx, billing.CreditPolicyInput{AccountID: "policy-account", Mode: billing.AccountPostpaid, Currency: "USD", CreditLimit: 40, SourceKey: "reduce", Reason: "unsafe"})
	if !errors.Is(err, billing.ErrUnsafeCreditLimitReduction) {
		t.Fatalf("unsafe policy = %v", err)
	}
	changed, err := store.ChangeCreditPolicy(ctx, billing.CreditPolicyInput{AccountID: "policy-account", Mode: billing.AccountPostpaid, Currency: "USD", CreditLimit: 150, SourceKey: "increase", Reason: "approved"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.ChangeCreditPolicy(ctx, billing.CreditPolicyInput{AccountID: "policy-account", Mode: billing.AccountPostpaid, Currency: "USD", CreditLimit: 150, SourceKey: "increase", Reason: "approved"})
	if err != nil || !replay.Replayed || replay.After.Version != changed.After.Version {
		t.Fatalf("policy replay = %+v, %v", replay, err)
	}
	if _, err := store.ChangeCreditPolicy(ctx, billing.CreditPolicyInput{AccountID: "policy-account", Mode: billing.AccountPostpaid, Currency: "USD", CreditLimit: 151, SourceKey: "increase", Reason: "approved"}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("policy conflict = %v", err)
	}
}

func TestSQLiteCreditPolicyIgnoresLegacyReservedForSpendable(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{
		ID: "policy-reserved", Currency: "USD", Mode: billing.AccountPostpaid,
		CreditLimit: 100, BalanceNano: 10, State: billing.AccountReady, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`UPDATE billing_accounts SET reserved_nano = 60, state = 'reconcile_required' WHERE account_id = ?`, "policy-reserved").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	// Floor-only affordability: CreditLimit 40 => floor -40; Balance 10 is still above floor.
	// Legacy reserved must not block the policy change; ready accounts reject nonzero reserved.
	_, err := store.ChangeCreditPolicy(ctx, billing.CreditPolicyInput{
		AccountID: "policy-reserved", Mode: billing.AccountPostpaid, Currency: "USD",
		CreditLimit: 40, SourceKey: "reduce-reserved", Reason: "safe-floor",
	})
	if !errors.Is(err, billing.ErrAccountNotReady) {
		t.Fatalf("policy on reconcile_required = %v, want ErrAccountNotReady", err)
	}
}

func TestSQLiteTrustedDebitRejectsReadyAccountWithLegacyReserved(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "reserved-debit", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`UPDATE billing_accounts SET reserved_nano = 100 WHERE account_id = ?`, "reserved-debit").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := store.PostAdjustment(ctx, billing.AdjustmentInput{
		AccountID: "reserved-debit", Amount: billing.Money{Nano: 100, Currency: "USD"},
		Direction: billing.AdjustmentDebit, SourceKey: "steal-reserved", Reason: "bad",
	})
	if !errors.Is(err, billing.ErrAccountInvalid) {
		t.Fatalf("debit with ready+reserved = %v, want ErrAccountInvalid", err)
	}
	var balance, reserved int64
	var version uint64
	if err := store.db.NewRaw(`SELECT balance_nano, reserved_nano, version FROM billing_accounts WHERE account_id = ?`, "reserved-debit").Scan(ctx, &balance, &reserved, &version); err != nil {
		t.Fatal(err)
	}
	if balance != 100 || reserved != 100 || version != 1 {
		t.Fatalf("account mutated by rejected debit: balance=%d reserved=%d version=%d", balance, reserved, version)
	}
}

func TestSQLiteFundingReplayFailsClosedWhenJournalMissing(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "evidence-account", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 10, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	input := billing.FundingInput{AccountID: "evidence-account", Amount: billing.Money{Nano: 5, Currency: "USD"}, SourceKey: "bank-missing", Reason: "topup"}
	fp, err := input.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	before := billing.AccountSnapshot{BalanceNano: 10, SpendableNano: 10, Mode: billing.AccountPrepaid, Currency: "USD", Version: 1}
	after := billing.AccountSnapshot{BalanceNano: 15, SpendableNano: 15, Mode: billing.AccountPrepaid, Currency: "USD", Version: 2}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{
		OperationKey: "funding:v1:bank-missing", AccountID: "evidence-account", OperationKind: "funding",
		SourceKey: "bank-missing", Fingerprint: fp, Before: before, After: after, SequenceStart: 1, SequenceEnd: 1,
	}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, err = store.PostFunding(ctx, input)
	if !errors.Is(err, ErrEvidenceIncomplete) {
		t.Fatalf("missing journal replay = %v, want ErrEvidenceIncomplete", err)
	}
}

func TestSQLiteConcurrentCreditPolicyChangesAreSerialized(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "policy-race", Currency: "USD", Mode: billing.AccountPostpaid, CreditLimit: 100, BalanceNano: 0, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	const workers = 24
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.ChangeCreditPolicy(ctx, billing.CreditPolicyInput{
				AccountID: "policy-race", Mode: billing.AccountPostpaid, Currency: "USD",
				CreditLimit: int64(150 + i), SourceKey: "policy-race-" + itoa(i), Reason: "approved",
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent policy change: %v", err)
		}
	}
	account, err := store.GetAccount(ctx, "policy-race")
	if err != nil {
		t.Fatal(err)
	}
	if account.Version != uint64(1+workers) {
		t.Fatalf("account version = %d, want %d", account.Version, 1+workers)
	}
}

func TestSQLiteTrustedOpsRejectReconcileRequired(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "blocked-ops", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 50, State: billing.AccountReconcileRequired, Version: 1}); err != nil {
		t.Fatal(err)
	}
	_, err := store.PostFunding(ctx, billing.FundingInput{AccountID: "blocked-ops", Amount: billing.Money{Nano: 1, Currency: "USD"}, SourceKey: "bank", Reason: "topup"})
	if !errors.Is(err, billing.ErrAccountNotReady) {
		t.Fatalf("funding blocked account = %v, want ErrAccountNotReady", err)
	}
}

func runTrustedFundingPaymentAdjustment(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPostpaid, CreditLimit: 100, BalanceNano: -20, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	funding, err := store.PostFunding(ctx, billing.FundingInput{AccountID: accountID, Amount: billing.Money{Nano: 30, Currency: "USD"}, SourceKey: "bank-1", Reason: "topup"})
	if err != nil {
		t.Fatal(err)
	}
	if funding.Before.BalanceNano != -20 || funding.After.BalanceNano != 10 || funding.After.Version != 2 || funding.Transaction.Book != billing.JournalBookFinancial {
		t.Fatalf("funding = %+v", funding)
	}
	replay, err := store.PostFunding(ctx, billing.FundingInput{AccountID: accountID, Amount: billing.Money{Nano: 30, Currency: "USD"}, SourceKey: "bank-1", Reason: "topup"})
	if err != nil || !replay.Replayed || replay.After.BalanceNano != 10 {
		t.Fatalf("funding replay = %+v, %v", replay, err)
	}
	if _, err := store.PostFunding(ctx, billing.FundingInput{AccountID: accountID, Amount: billing.Money{Nano: 31, Currency: "USD"}, SourceKey: "bank-1", Reason: "topup"}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("funding conflict = %v", err)
	}
	payment, err := store.PostPayment(ctx, billing.PaymentInput{AccountID: accountID, Amount: billing.Money{Nano: 5, Currency: "USD"}, SourceKey: "payment-1", Reason: "settlement"})
	if err != nil || payment.After.BalanceNano != 15 {
		t.Fatalf("payment = %+v, %v", payment, err)
	}
	adjustment, err := store.PostAdjustment(ctx, billing.AdjustmentInput{AccountID: accountID, Amount: billing.Money{Nano: 3, Currency: "USD"}, Direction: billing.AdjustmentDebit, SourceKey: "adjust-1", Reason: "correction"})
	if err != nil || adjustment.After.BalanceNano != 12 {
		t.Fatalf("adjustment = %+v, %v", adjustment, err)
	}
	policy, err := store.ChangeCreditPolicy(ctx, billing.CreditPolicyInput{AccountID: accountID, Mode: billing.AccountPostpaid, Currency: "USD", CreditLimit: 200, SourceKey: "policy-1", Reason: "approved"})
	if err != nil || policy.After.CreditLimitNano != 200 || policy.After.Version != 5 {
		t.Fatalf("policy = %+v, %v", policy, err)
	}
	if got, err := store.GetAccount(ctx, accountID); err != nil || got.BalanceNano != 12 || got.CreditLimit != 200 || got.Version != 5 {
		t.Fatalf("account = %+v, %v", got, err)
	}
	journals, err := store.JournalTransactions(ctx, accountID)
	if err != nil || len(journals) != 3 {
		t.Fatalf("trusted journals = %d, %v", len(journals), err)
	}
	for _, journal := range journals {
		if err := journal.Validate(); err != nil {
			t.Fatalf("journal invalid: %v", err)
		}
	}
}
