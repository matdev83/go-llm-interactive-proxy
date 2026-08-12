package billingstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteReconcileAccountRebuildsSettlementStateDeterministically(t *testing.T) {
	runReconcileAccountRebuildsSettlement(t, newSQLiteTestStore(t), "reconcile-account")
}

func TestSQLiteReconcileRequiredBlocksAdmissionUntilVerifiedRepair(t *testing.T) {
	runReconcileRequiredBlocksAdmission(t, newSQLiteTestStore(t), "blocked-reconcile")
}

func runReconcileAccountRebuildsSettlement(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authorize(ctx, authorizationInput(accountID, "turn", "auth", 40))
	if err != nil {
		t.Fatal(err)
	}
	record := settlementStoreRecord(accountID, "turn", "auth", billing.MoneyEvidence{NanoUnits: 7, Currency: "USD", Present: true})
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsageRecord(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	result := billing.BillingResult{TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 12, Currency: "USD"}, OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 7, Currency: "USD"}, AmountPresent: true, Reconciled: true}}}
	if _, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: result}); err != nil {
		t.Fatal(err)
	}
	report, err := store.ReconcileAccount(ctx, accountID)
	if err != nil || !report.OK || report.Rebuilt.BalanceNano != 88 || report.Rebuilt.ReservedNano != 0 {
		t.Fatalf("reconcile = %+v, %v", report, err)
	}
}

func runReconcileRequiredBlocksAdmission(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PostFunding(ctx, billing.FundingInput{AccountID: accountID, Amount: billing.Money{Nano: 10, Currency: "USD"}, SourceKey: "fund", Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`UPDATE billing_accounts SET balance_nano = 999, version = 99 WHERE account_id = ?`, accountID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	report, err := store.ReconcileAccount(ctx, accountID)
	if err == nil || report.OK {
		t.Fatalf("corrupt reconcile = %+v, %v", report, err)
	}
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.State != billing.AccountReconcileRequired {
		t.Fatalf("state after corruption = %+v", account)
	}
	if _, err := store.Authorize(ctx, authorizationInput(accountID, "blocked-turn", "blocked-auth", 1)); !errors.Is(err, billing.ErrAccountNotReady) {
		t.Fatalf("blocked authorization = %v", err)
	}
	verified, err := store.ReconcileAccount(ctx, accountID)
	if err != nil || !verified.OK {
		t.Fatalf("verified reconcile = %+v, %v", verified, err)
	}
	var auditEvents int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliation_events WHERE account_id = ?`, accountID).Scan(ctx, &auditEvents); err != nil {
		t.Fatal(err)
	}
	if auditEvents != 2 {
		t.Fatalf("reconciliation audit events = %d, want blocked and verified transitions", auditEvents)
	}
	account, err = store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.State != billing.AccountReady || account.BalanceNano != 110 || account.Version != 2 {
		t.Fatalf("re-enabled account = %+v", account)
	}
	if _, err := store.Authorize(ctx, authorizationInput(accountID, "after-turn", "after-auth", 1)); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteReconcileAccountMissingOpeningIsIntegrityFailure(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := store.db.NewRaw(`INSERT INTO billing_accounts(account_id, currency, mode, credit_limit_nano, balance_nano, opening_balance_nano, reserved_nano, version, state, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		"missing-opening", "USD", string(billing.AccountPrepaid), int64(0), int64(50), int64(50), int64(0), uint64(1), string(billing.AccountReady), now, now).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	report, err := store.ReconcileAccount(ctx, "missing-opening")
	if err == nil || report.OK {
		t.Fatalf("missing opening reconcile = %+v, %v", report, err)
	}
	account, err := store.GetAccount(ctx, "missing-opening")
	if err != nil {
		t.Fatal(err)
	}
	if account.State != billing.AccountReconcileRequired {
		t.Fatalf("missing opening must prove integrity failure: %+v", account)
	}
}

func TestSQLiteReconcileAccountDetectsFingerprintCorruption(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "fp-corrupt", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PostFunding(ctx, billing.FundingInput{AccountID: "fp-corrupt", Amount: billing.Money{Nano: 5, Currency: "USD"}, SourceKey: "fund-fp", Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER IF EXISTS billing_journal_tx_immutable_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`UPDATE journal_transactions SET semantic_fingerprint = 'tampered' WHERE account_id = ?`, "fp-corrupt").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	report, err := store.ReconcileAccount(ctx, "fp-corrupt")
	if err == nil || report.OK {
		t.Fatalf("fingerprint corruption reconcile = %+v, %v", report, err)
	}
	account, err := store.GetAccount(ctx, "fp-corrupt")
	if err != nil {
		t.Fatal(err)
	}
	if account.State != billing.AccountReconcileRequired {
		t.Fatalf("fingerprint corruption state = %+v", account)
	}
	if _, err := store.ReconcileAccount(ctx, "fp-corrupt"); err == nil {
		t.Fatal("tampered journals must not clear reconcile_required")
	}
}

func TestSQLiteReconcileAccountAllowsConcurrentAuthorize(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID = "reconcile-concurrent"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		input := journalInput("tx-hist-"+itoa(i), "usage-hist-"+itoa(i), 1)
		input.AccountID = accountID
		if _, err := store.postJournalTransaction(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := store.ReconcileAccount(ctx, accountID)
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := store.Authorize(ctx, authorizationInput(accountID, "turn-live", "auth-live", 10))
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent reconcile/authorize: %v", err)
		}
	}
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedNano != 10 || account.State != billing.AccountReady {
		t.Fatalf("account after concurrent reconcile = %+v", account)
	}
}

func TestSQLiteZeroEffectSettlementReconcilesAlongsideOtherJournals(t *testing.T) {
	t.Run("funding then zero-effect", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		ctx := context.Background()
		const accountID = "zero-effect-after-fund"
		if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.PostFunding(ctx, billing.FundingInput{AccountID: accountID, Amount: billing.Money{Nano: 10, Currency: "USD"}, SourceKey: "fund", Reason: "test"}); err != nil {
			t.Fatal(err)
		}
		settleZeroEffectTurn(t, store, accountID, "zero-turn", "zero-auth")
		report, err := store.ReconcileAccount(ctx, accountID)
		if err != nil || !report.OK {
			t.Fatalf("zero-effect after funding reconcile = %+v, %v", report, err)
		}
	})
	t.Run("zero-effect then funding", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		ctx := context.Background()
		const accountID = "zero-effect-before-fund"
		if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
			t.Fatal(err)
		}
		settleZeroEffectTurn(t, store, accountID, "zero-turn", "zero-auth")
		if _, err := store.PostFunding(ctx, billing.FundingInput{AccountID: accountID, Amount: billing.Money{Nano: 10, Currency: "USD"}, SourceKey: "fund", Reason: "test"}); err != nil {
			t.Fatal(err)
		}
		report, err := store.ReconcileAccount(ctx, accountID)
		if err != nil || !report.OK {
			t.Fatalf("funding after zero-effect reconcile = %+v, %v", report, err)
		}
	})
	t.Run("zero-effect then later charged settlement", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		ctx := context.Background()
		const accountID = "zero-effect-then-charge"
		if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
			t.Fatal(err)
		}
		settleZeroEffectTurn(t, store, accountID, "zero-turn", "zero-auth")
		auth, err := store.Authorize(ctx, authorizationInput(accountID, "paid-turn", "paid-auth", 40))
		if err != nil {
			t.Fatal(err)
		}
		record := settlementStoreRecord(accountID, "paid-turn", "paid-auth", billing.MoneyEvidence{NanoUnits: 3, Currency: "USD", Present: true})
		sealed, err := record.Seal()
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AppendUsageRecord(ctx, sealed); err != nil {
			t.Fatal(err)
		}
		result := billing.BillingResult{TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 8, Currency: "USD"}, OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 3, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}}}
		if _, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: result}); err != nil {
			t.Fatal(err)
		}
		report, err := store.ReconcileAccount(ctx, accountID)
		if err != nil || !report.OK {
			t.Fatalf("charged settlement after zero-effect reconcile = %+v, %v", report, err)
		}
	})
}

func TestSQLitePostedJournalWithZeroSequenceSnapshotIsIntegrityFailure(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID = "zero-seq-corrupt"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authorize(ctx, authorizationInput(accountID, "turn", "auth", 40))
	if err != nil {
		t.Fatal(err)
	}
	record := settlementStoreRecord(accountID, "turn", "auth", billing.MoneyEvidence{NanoUnits: 3, Currency: "USD", Present: true})
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsageRecord(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	result := billing.BillingResult{TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 8, Currency: "USD"}, OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 3, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}}}
	if _, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: result}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER IF EXISTS billing_operation_snapshots_immutable_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`UPDATE billing_operation_snapshots SET account_sequence_start = 0, account_sequence_end = 0, integrity_fingerprint = '' WHERE account_id = ? AND operation_kind = 'customer_settlement'`, accountID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	report, err := store.ReconcileAccount(ctx, accountID)
	if err == nil || report.OK {
		t.Fatalf("posted journal with zero-sequence snapshot reconcile = %+v, %v", report, err)
	}
	found := false
	for _, issue := range report.Issues {
		if issue.Code == "snapshot_sequence_missing" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("issues = %+v, want snapshot_sequence_missing", report.Issues)
	}
}

func settleZeroEffectTurn(t *testing.T, store *DurableStore, accountID, turnID, authID string) {
	t.Helper()
	ctx := context.Background()
	auth, err := store.Authorize(ctx, authorizationInput(accountID, turnID, authID, 0))
	if err != nil {
		t.Fatal(err)
	}
	record := settlementStoreRecord(accountID, turnID, authID, billing.MoneyEvidence{NanoUnits: 0, Currency: "USD", Present: true})
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsageRecord(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	result := billing.BillingResult{TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 0, Currency: "USD"}, OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 0, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}}}
	if _, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: result}); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteReplayAccountMatchesDeterministicOperationFixtures(t *testing.T) {
	for _, fixture := range replayStoreFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			runReplayStoreFixture(t, newSQLiteTestStore(t), "replay-acct", fixture)
		})
	}
}

type replayStoreOpKind string

const (
	replayStoreFund           replayStoreOpKind = "fund"
	replayStoreAuthorize      replayStoreOpKind = "authorize"
	replayStoreSettle         replayStoreOpKind = "settle"
	replayStoreRelease        replayStoreOpKind = "release"
	replayStoreReverseReplace replayStoreOpKind = "reverse_replace"
)

type replayStoreOp struct {
	kind replayStoreOpKind
	nano int64
}

type replayStoreFixture struct {
	name        string
	mode        billing.AccountMode
	opening     int64
	creditLimit int64
	ops         []replayStoreOp
}

func replayStoreFixtures() []replayStoreFixture {
	fixtures := []replayStoreFixture{
		{name: "prepaid fund authorize settle", mode: billing.AccountPrepaid, opening: 100, ops: []replayStoreOp{
			{kind: replayStoreFund, nano: 40},
			{kind: replayStoreAuthorize, nano: 30},
			{kind: replayStoreSettle, nano: 12},
		}},
		{name: "prepaid unused hold release", mode: billing.AccountPrepaid, opening: 50, ops: []replayStoreOp{
			{kind: replayStoreAuthorize, nano: 20},
			{kind: replayStoreRelease, nano: 0},
			{kind: replayStoreFund, nano: 15},
		}},
		{name: "prepaid reverse replace funding", mode: billing.AccountPrepaid, opening: 80, ops: []replayStoreOp{
			{kind: replayStoreFund, nano: 25},
			{kind: replayStoreReverseReplace, nano: 10},
			{kind: replayStoreAuthorize, nano: 5},
			{kind: replayStoreSettle, nano: 5},
		}},
		{name: "postpaid fund charge stays above floor", mode: billing.AccountPostpaid, opening: 0, creditLimit: 50, ops: []replayStoreOp{
			{kind: replayStoreFund, nano: 10},
			{kind: replayStoreAuthorize, nano: 40},
			{kind: replayStoreSettle, nano: 30},
		}},
		{name: "postpaid authorize release then settle sibling", mode: billing.AccountPostpaid, opening: -10, creditLimit: 100, ops: []replayStoreOp{
			{kind: replayStoreAuthorize, nano: 20},
			{kind: replayStoreRelease, nano: 0},
			{kind: replayStoreAuthorize, nano: 15},
			{kind: replayStoreSettle, nano: 8},
		}},
		{name: "repeated fund authorize settle", mode: billing.AccountPrepaid, opening: 200, ops: []replayStoreOp{
			{kind: replayStoreFund, nano: 5},
			{kind: replayStoreAuthorize, nano: 9},
			{kind: replayStoreSettle, nano: 3},
			{kind: replayStoreFund, nano: 7},
			{kind: replayStoreAuthorize, nano: 4},
			{kind: replayStoreSettle, nano: 4},
			{kind: replayStoreFund, nano: 1},
		}},
		{name: "zero remaining settle closes hold", mode: billing.AccountPrepaid, opening: 60, ops: []replayStoreOp{
			{kind: replayStoreAuthorize, nano: 11},
			{kind: replayStoreSettle, nano: 11},
		}},
		{name: "replace then authorize from net funding", mode: billing.AccountPrepaid, opening: 30, ops: []replayStoreOp{
			{kind: replayStoreFund, nano: 20},
			{kind: replayStoreReverseReplace, nano: 8},
			{kind: replayStoreAuthorize, nano: 8},
			{kind: replayStoreRelease, nano: 0},
		}},
	}
	funds := []int64{5, 12, 25}
	holds := []int64{4, 9, 15}
	charges := []int64{1, 4, 9}
	for _, fund := range funds {
		for _, hold := range holds {
			for _, charge := range charges {
				if charge > hold {
					continue
				}
				fixtures = append(fixtures, replayStoreFixture{
					name:    fmt.Sprintf("prepaid fund=%d hold=%d charge=%d", fund, hold, charge),
					mode:    billing.AccountPrepaid,
					opening: 80,
					ops: []replayStoreOp{
						{kind: replayStoreFund, nano: fund},
						{kind: replayStoreAuthorize, nano: hold},
						{kind: replayStoreSettle, nano: charge},
					},
				})
			}
		}
	}
	return fixtures
}

func runReplayStoreFixture(t *testing.T, store *DurableStore, accountID string, fixture replayStoreFixture) {
	t.Helper()
	ctx := context.Background()
	account := billing.Account{
		ID: accountID, Currency: "USD", Mode: fixture.mode, CreditLimit: fixture.creditLimit,
		BalanceNano: fixture.opening, State: billing.AccountReady, Version: 1,
	}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	var lastFunding billing.JournalTransaction
	var openAuth billing.Authorization
	openHold := false
	for i, op := range fixture.ops {
		switch op.kind {
		case replayStoreFund:
			posting, err := store.PostFunding(ctx, billing.FundingInput{
				AccountID: accountID, Amount: billing.Money{Nano: op.nano, Currency: "USD"},
				SourceKey: "fund-" + strconv.Itoa(i), Reason: "replay-fixture",
			})
			if err != nil {
				t.Fatalf("op %d fund: %v", i, err)
			}
			lastFunding = posting.Transaction
		case replayStoreAuthorize:
			if openHold {
				t.Fatalf("op %d authorize while hold is open", i)
			}
			turnID := "turn-" + strconv.Itoa(i)
			authID := "auth-" + strconv.Itoa(i)
			auth, err := store.Authorize(ctx, authorizationInput(accountID, turnID, authID, op.nano))
			if err != nil {
				t.Fatalf("op %d authorize: %v", i, err)
			}
			openAuth = auth
			openHold = true
		case replayStoreSettle:
			if !openHold {
				t.Fatalf("op %d settle with no hold", i)
			}
			turnID, ok := strings.CutPrefix(openAuth.TURKey, accountID+":")
			if !ok || turnID == "" {
				t.Fatalf("op %d authorization TUR key %q is not bound to account %q", i, openAuth.TURKey, accountID)
			}
			record := settlementStoreRecord(accountID, turnID, openAuth.ID, billing.MoneyEvidence{NanoUnits: 1, Currency: "USD", Present: true})
			sealed, err := record.Seal()
			if err != nil {
				t.Fatal(err)
			}
			if err := store.AppendUsageRecord(ctx, sealed); err != nil {
				t.Fatalf("op %d append TUR: %v", i, err)
			}
			result := billing.BillingResult{
				TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: op.nano, Currency: "USD"},
				OperatorCosts: []billing.OperatorCostResult{{
					LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 1, Currency: "USD"},
					AmountPresent: true, Reconciled: true, Authoritative: true,
				}},
			}
			if _, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: openAuth, Result: result}); err != nil {
				t.Fatalf("op %d settle: %v", i, err)
			}
			openHold = false
		case replayStoreRelease:
			if !openHold {
				t.Fatalf("op %d release with no hold", i)
			}
			if _, err := store.ReleaseAuthorization(ctx, billing.ReleaseAuthorizationInput{
				AccountID: accountID, AuthorizationID: openAuth.ID, TURKey: openAuth.TURKey,
				FullClose: true, Reason: billing.ReleaseExecutionNotStarted, SourceKey: "release-" + strconv.Itoa(i),
			}); err != nil {
				t.Fatalf("op %d release: %v", i, err)
			}
			openHold = false
		case replayStoreReverseReplace:
			if lastFunding.ID == "" {
				t.Fatalf("op %d reverse_replace without prior funding", i)
			}
			if err := postCorrectionPair(t, store, lastFunding, op.nano); err != nil {
				t.Fatalf("op %d reverse_replace: %v", i, err)
			}
			repairMaterializedFromJournal(t, store, accountID)
		default:
			t.Fatalf("unknown op %q", op.kind)
		}
		assertReplayMatchesAccount(t, store, accountID, fixture.opening, i)
	}
}

func repairMaterializedFromJournal(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	report, err := store.ReconcileAccount(ctx, accountID)
	if err == nil && report.OK {
		return
	}
	report, err = store.ReconcileAccount(ctx, accountID)
	if err != nil || !report.OK {
		t.Fatalf("reconcile after reverse+replace = %+v, %v", report, err)
	}
}

func assertReplayMatchesAccount(t *testing.T, store *DurableStore, accountID string, opening int64, opIndex int) {
	t.Helper()
	ctx := context.Background()
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.Mode == billing.AccountPrepaid && account.BalanceNano < 0 {
		t.Fatalf("after op %d prepaid balance %d < 0", opIndex, account.BalanceNano)
	}
	if account.Mode == billing.AccountPostpaid && account.BalanceNano < -account.CreditLimit {
		t.Fatalf("after op %d postpaid balance %d < -creditLimit %d", opIndex, account.BalanceNano, account.CreditLimit)
	}
	journals, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	report := billing.ReplayAccount(account, opening, journals)
	if !report.OK {
		t.Fatalf("after op %d replay issues = %+v", opIndex, report.Issues)
	}
	spendable, err := account.SpendableNano()
	if err != nil {
		t.Fatal(err)
	}
	if report.Rebuilt.BalanceNano != account.BalanceNano || report.Rebuilt.ReservedNano != account.ReservedNano || report.Rebuilt.SpendableNano != spendable {
		t.Fatalf("after op %d rebuilt=%+v account balance=%d reserved=%d spendable=%d", opIndex, report.Rebuilt, account.BalanceNano, account.ReservedNano, spendable)
	}
}
