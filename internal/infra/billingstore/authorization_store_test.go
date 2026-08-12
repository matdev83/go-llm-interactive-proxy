package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func authorizationInput(accountID, turnID, authID string, amount int64) billing.AuthorizeInput {
	return billing.AuthorizeInput{
		ID: authID, AccountID: accountID, TURKey: accountID + ":" + turnID,
		MaxCustomerCharge: billing.MaxCostBound{
			Amount:          billing.Money{Nano: amount, Currency: "USD"},
			PricingRef:      billing.VersionRef{ID: "pricing", Version: "v1"},
			ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
		},
	}
}

func TestSQLiteAuthorizeCreatesAtomicHoldJournalAndSnapshots(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "auth-acct", Currency: "USD", Mode: billing.AccountPrepaid, State: billing.AccountReady, BalanceNano: 100, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authorize(ctx, authorizationInput("auth-acct", "turn-1", "auth-1", 40))
	if err != nil {
		t.Fatal(err)
	}
	if auth.Before.SpendableNano != 100 || auth.After.SpendableNano != 60 || auth.After.ReservedNano != 40 || auth.After.Version != 2 {
		t.Fatalf("snapshots = before %+v after %+v", auth.Before, auth.After)
	}
	account, err := store.GetAccount(ctx, "auth-acct")
	if err != nil {
		t.Fatal(err)
	}
	if account.BalanceNano != 100 || account.ReservedNano != 40 || account.Version != 2 {
		t.Fatalf("account after hold = %+v", account)
	}
	journals, err := store.JournalTransactions(ctx, "auth-acct")
	if err != nil {
		t.Fatal(err)
	}
	if len(journals) != 1 || journals[0].Book != billing.JournalBookAuthorization || journals[0].Entries[0].Amount.Nano != 40 {
		t.Fatalf("authorization journal = %+v", journals)
	}
	var snapshots int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM billing_operation_snapshots WHERE account_id = ? AND operation_kind = ?`, "auth-acct", "authorization").Scan(ctx, &snapshots); err != nil {
		t.Fatal(err)
	}
	if snapshots != 1 {
		t.Fatalf("authorization operation snapshots = %d, want 1", snapshots)
	}
}

func TestSQLiteAuthorizeAfterFundingReconcilesWithoutSnapshotMismatch(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "fund-auth", Currency: "USD", Mode: billing.AccountPrepaid, State: billing.AccountReady, BalanceNano: 100, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PostFunding(ctx, billing.FundingInput{AccountID: "fund-auth", Amount: billing.Money{Nano: 10, Currency: "USD"}, SourceKey: "bank-1", Reason: "topup"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authorize(ctx, authorizationInput("fund-auth", "turn-1", "auth-1", 40)); err != nil {
		t.Fatal(err)
	}
	report, err := store.ReconcileAccount(ctx, "fund-auth")
	if err != nil || !report.OK {
		t.Fatalf("reconcile after funding+authorize = %+v, %v", report, err)
	}
	for _, issue := range report.Issues {
		if issue.Code == "snapshot_version_mismatch" {
			t.Fatalf("authorization must persist an operation snapshot: %+v", report.Issues)
		}
	}
	account, err := store.GetAccount(ctx, "fund-auth")
	if err != nil {
		t.Fatal(err)
	}
	if account.State != billing.AccountReady || account.ReservedNano != 40 || account.BalanceNano != 110 {
		t.Fatalf("account after reconcile = %+v", account)
	}
}

func TestSQLiteAuthorizeReplayDoesNotReserveTwiceAndConflictIsAtomic(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "replay-acct", Currency: "USD", Mode: billing.AccountPostpaid, CreditLimit: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	in := authorizationInput("replay-acct", "turn-1", "auth-1", 40)
	first, err := store.Authorize(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Authorize(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if second.Fingerprint != first.Fingerprint || second.After.ReservedNano != 40 || second.Status != billing.HoldStatusOpen {
		t.Fatalf("replay = %+v", second)
	}
	boundMismatch := in
	boundMismatch.Amount = billing.Money{Nano: 41, Currency: "USD"}
	if _, err := store.Authorize(ctx, boundMismatch); !errors.Is(err, billing.ErrAuthorizationInvalid) {
		t.Fatalf("bound mismatch = %v, want ErrAuthorizationInvalid", err)
	}
	conflict := authorizationInput("replay-acct", "turn-1", "auth-1", 41)
	if _, err := store.Authorize(ctx, conflict); !errors.Is(err, billing.ErrAuthorizationConflict) {
		t.Fatalf("fingerprint conflict = %v, want ErrAuthorizationConflict", err)
	}
	account, err := store.GetAccount(ctx, "replay-acct")
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedNano != 40 || account.Version != 2 {
		t.Fatalf("conflicting replay mutated account = %+v", account)
	}
}

func TestSQLiteAuthorizeRejectsCurrencyMismatchWithoutMutation(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "eur-acct", Currency: "EUR", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authorize(ctx, authorizationInput("eur-acct", "turn", "auth-eur", 10)); !errors.Is(err, billing.ErrMoneyCurrencyMismatch) {
		t.Fatalf("currency mismatch = %v, want ErrMoneyCurrencyMismatch", err)
	}
	account, err := store.GetAccount(ctx, "eur-acct")
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedNano != 0 || account.Version != 1 {
		t.Fatalf("currency mismatch mutated account = %+v", account)
	}
}

func TestSQLiteAuthorizeRejectsClosedAndExpiredHoldReplay(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "closed-acct", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	in := authorizationInput("closed-acct", "turn-closed", "auth-closed", 10)
	if _, err := store.Authorize(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseAuthorization(ctx, billing.ReleaseAuthorizationInput{
		AccountID: "closed-acct", AuthorizationID: "auth-closed", TURKey: in.TURKey,
		FullClose: true, Reason: billing.ReleaseExecutionNotStarted, SourceKey: "release-closed-turn",
		Amount: billing.Money{Currency: "USD"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authorize(ctx, in); !errors.Is(err, billing.ErrAuthorizationClosed) {
		t.Fatalf("closed replay = %v, want ErrAuthorizationClosed", err)
	}
	account, err := store.GetAccount(ctx, "closed-acct")
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedNano != 0 {
		t.Fatalf("closed account reserved = %d, want 0", account.ReservedNano)
	}

	expiredInput := authorizationInput("closed-acct", "turn-expired", "auth-expired", 5)
	expiredInput.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	if _, err := store.Authorize(ctx, expiredInput); !errors.Is(err, billing.ErrAuthorizationExpired) {
		t.Fatalf("create expired = %v, want ErrAuthorizationExpired", err)
	}
	openThenExpire := authorizationInput("closed-acct", "turn-open-expire", "auth-open-expire", 5)
	openThenExpire.ExpiresAt = time.Now().UTC().Add(30 * time.Millisecond)
	if _, err := store.Authorize(ctx, openThenExpire); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, err := store.Authorize(ctx, openThenExpire); !errors.Is(err, billing.ErrAuthorizationExpired) {
		t.Fatalf("expired replay = %v, want ErrAuthorizationExpired", err)
	}
}

func TestAuthorizationFromRowRejectsLegacySnapshotlessHold(t *testing.T) {
	_, err := authorizationFromRow(authorizationHoldRow{})
	if !errors.Is(err, billing.ErrLegacyAuthorization) || !errors.Is(err, billing.ErrAuthorizationInvalid) {
		t.Fatalf("legacy hold error = %v, want legacy and invalid classifications", err)
	}
}

func TestSQLiteAuthorizeRejectsAccountVersionOverflowWithoutMutation(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "overflow-auth", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 10, State: billing.AccountReady, Version: uint64(^uint64(0) >> 1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authorize(ctx, authorizationInput("overflow-auth", "turn", "overflow-id", 1)); !errors.Is(err, billing.ErrAuthorizationInvalid) {
		t.Fatalf("overflow error = %v, want ErrAuthorizationInvalid", err)
	}
	account, err := store.GetAccount(ctx, "overflow-auth")
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedNano != 0 || account.Version != uint64(^uint64(0)>>1) {
		t.Fatalf("overflow account mutated = %+v", account)
	}
}

func TestSQLiteAuthorizeRejectsInsufficientAndReconcileRequiredWithoutMutation(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	for _, account := range []billing.Account{
		{ID: "small-prepaid", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 10, State: billing.AccountReady, Version: 1},
		{ID: "small-postpaid", Currency: "USD", Mode: billing.AccountPostpaid, CreditLimit: 10, BalanceNano: -5, State: billing.AccountReady, Version: 1},
		{ID: "blocked", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReconcileRequired, Version: 1},
	} {
		if err := store.CreateAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		account, turn string
		amount        int64
	}{
		{"small-prepaid", "p", 11}, {"small-postpaid", "p", 6}, {"blocked", "p", 1},
	} {
		if _, err := store.Authorize(ctx, authorizationInput(tc.account, tc.turn, tc.account+"-auth", tc.amount)); !errors.Is(err, billing.ErrInsufficientSpendable) && !errors.Is(err, billing.ErrAccountNotReady) {
			t.Fatalf("%s error = %v", tc.account, err)
		}
		account, getErr := store.GetAccount(ctx, tc.account)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if account.ReservedNano != 0 || account.Version != 1 {
			t.Fatalf("%s mutated after denial = %+v", tc.account, account)
		}
	}
}

func TestSQLiteAuthorizeReplayRejectsReconcileRequired(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "replay-ready", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	in := authorizationInput("replay-ready", "turn-1", "auth-1", 40)
	if _, err := store.Authorize(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccountReconcileRequired(ctx, "replay-ready"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authorize(ctx, in); !errors.Is(err, billing.ErrAccountNotReady) {
		t.Fatalf("replay while reconcile_required = %v, want ErrAccountNotReady", err)
	}
}

func TestSQLiteAuthorizeUnavailableOnClosedStore(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "closed-store", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := store.Authorize(ctx, authorizationInput("closed-store", "turn", "auth-closed-store", 10))
	if !errors.Is(err, billing.ErrAuthorizationUnavailable) {
		t.Fatalf("closed store authorize = %v, want ErrAuthorizationUnavailable", err)
	}
	if errors.Is(err, billing.ErrInsufficientSpendable) || errors.Is(err, billing.ErrAccountNotReady) {
		t.Fatalf("store failure must not look like a spendable denial: %v", err)
	}
}
