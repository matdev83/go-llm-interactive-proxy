package billingstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	_ "modernc.org/sqlite"
)

func TestSQLiteSubmissionFeeSettlementIsClaimedOnceConcurrently(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "submission-concurrent", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}

	type pending struct {
		call     billing.CallUsageRecord
		exposure billing.CallExposure
		result   billing.CallRatingResult
	}
	pendingCalls := make([]pending, 2)
	for i := range pendingCalls {
		call, exposure := phase10SubmissionCall(t, store, account.ID, "submission-concurrent", fmt.Sprintf("a-concurrent-%d", i))
		pendingCalls[i] = pending{
			call:     call,
			exposure: exposure,
			result: billing.CallRatingResult{
				CallID: call.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"},
				Fingerprint:       fmt.Sprintf("submission-concurrent-result-%d", i),
				CustomerValuation: phase10SubmissionValuation(t, call, 5, 10, "tariff-v1", "policy-v1"),
			},
		}
	}

	errs := make(chan error, len(pendingCalls))
	var group sync.WaitGroup
	for _, item := range pendingCalls {
		group.Go(func() {
			_, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: item.call, Exposure: item.exposure, Result: item.result})
			errs <- err
		})
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent settlement: %v", err)
		}
	}

	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 975 {
		t.Fatalf("balance after two calls = %d, want 975 (15 + 10; submission fee once)", got.BalanceNano)
	}
	var claims int
	if err := store.db.NewRaw(`SELECT COUNT(*) FROM billing_submission_fee_claims WHERE store_id = ? AND account_id = ? AND submission_id = ?`, "test", account.ID, "submission-concurrent").Scan(ctx, &claims); err != nil {
		t.Fatal(err)
	}
	if claims != 1 {
		t.Fatalf("submission fee claims = %d, want one", claims)
	}
}

func TestSQLiteSubmissionFeeSettlementReplayAndScopes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteTestStore(t)
	account := billing.Account{ID: "submission-scopes-a", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	otherAccount := billing.Account{ID: "submission-scopes-b", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	for _, item := range []billing.Account{account, otherAccount} {
		if err := store.CreateAccount(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	firstCall, firstExposure := phase10SubmissionCall(t, store, account.ID, "submission-replay", "a-replay-1")
	firstResult := billing.CallRatingResult{CallID: firstCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-replay-1", CustomerValuation: phase10SubmissionValuation(t, firstCall, 5, 10, "tariff-v1", "policy-v1")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: firstCall, Exposure: firstExposure, Result: firstResult}); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: firstCall, Exposure: firstExposure, Result: firstResult})
	if err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("replay result = %+v, want Replayed", replayed)
	}
	conflictingReplay := firstResult
	conflictingReplay.CustomerValuation = phase10SubmissionValuation(t, firstCall, 5, 10, "tariff-v2", "policy-v1")
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: firstCall, Exposure: firstExposure, Result: conflictingReplay}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("same-call tariff conflict = %v, want ErrOperationConflict", err)
	}
	conflictingReplay.CustomerValuation = phase10SubmissionValuation(t, firstCall, 5, 10, "tariff-v1", "policy-v2")
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: firstCall, Exposure: firstExposure, Result: conflictingReplay}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("same-call policy conflict = %v, want ErrOperationConflict", err)
	}

	secondCall, secondExposure := phase10SubmissionCall(t, store, account.ID, "submission-replay", "a-replay-2")
	secondResult := billing.CallRatingResult{CallID: secondCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-replay-2", CustomerValuation: phase10SubmissionValuation(t, secondCall, 5, 10, "tariff-v1", "policy-v1")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: secondCall, Exposure: secondExposure, Result: secondResult}); err != nil {
		t.Fatal(err)
	}

	otherCall, otherExposure := phase10SubmissionCall(t, store, otherAccount.ID, "submission-replay", "a-replay-other-account")
	otherResult := billing.CallRatingResult{CallID: otherCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-replay-other-account", CustomerValuation: phase10SubmissionValuation(t, otherCall, 5, 10, "tariff-v1", "policy-v1")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: otherCall, Exposure: otherExposure, Result: otherResult}); err != nil {
		t.Fatal(err)
	}
	newSubmissionCall, newSubmissionExposure := phase10SubmissionCall(t, store, account.ID, "submission-new", "a-replay-new")
	newSubmissionResult := billing.CallRatingResult{CallID: newSubmissionCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-replay-new", CustomerValuation: phase10SubmissionValuation(t, newSubmissionCall, 5, 10, "tariff-v1", "policy-v1")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: newSubmissionCall, Exposure: newSubmissionExposure, Result: newSubmissionResult}); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 960 {
		t.Fatalf("same-account balance = %d, want 960 (15 + 10 + 15)", got.BalanceNano)
	}
	got, err = store.GetAccount(ctx, otherAccount.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 985 {
		t.Fatalf("other-account balance = %d, want 985", got.BalanceNano)
	}
	var claimKey string
	if err := store.db.NewRaw(`SELECT claim_key FROM billing_submission_fee_claims WHERE store_id = ? AND account_id = ? AND submission_id = ?`, "test", account.ID, "submission-replay").Scan(ctx, &claimKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`UPDATE billing_submission_fee_claims SET amount_nano = amount_nano + 1 WHERE claim_key = ?`, claimKey).Exec(ctx); err == nil {
		t.Fatal("submission fee claim update unexpectedly succeeded")
	}
	if _, err := store.db.NewRaw(`DELETE FROM billing_submission_fee_claims WHERE claim_key = ?`, claimKey).Exec(ctx); err == nil {
		t.Fatal("submission fee claim delete unexpectedly succeeded")
	}
}

func TestSQLiteSubmissionFeeOverrunReplayAfterClaimPreservesOwnerOutcome(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteTestStore(t)
	account := billing.Account{ID: "submission-overrun-replay", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	call, exposure := phase10SubmissionCallWithMax(t, store, account.ID, "submission-overrun", "a-overrun", 100)
	valuation := phase10SubmissionValuation(t, call, 20, 100, "tariff-v1", "policy-v1")
	result := billing.CallRatingResult{
		CallID: call.CallID, CustomerCharge: billing.Money{Nano: 120, Currency: "USD"},
		Fingerprint: valuation.Fingerprint(), CustomerValuation: valuation,
	}
	first, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result})
	if err != nil {
		t.Fatalf("first overrun settlement: %v", err)
	}
	if first.Replayed || !first.Breached || first.OverrunNano != 20 {
		t.Fatalf("first settlement = %+v, want fresh 20-nano overrun", first)
	}
	if first.Customer.Transaction.ID == "" {
		t.Fatalf("first settlement missing journal transaction: %+v", first)
	}

	second, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result})
	if err != nil {
		t.Fatalf("exact overrun replay after submission claim: %v", err)
	}
	if !second.Replayed || !second.Breached || second.OverrunNano != 20 {
		t.Fatalf("replay settlement = %+v, want identical disposition", second)
	}
}

func TestSQLiteSubmissionFeeZeroPricedSiblingSettlesWithoutConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteTestStore(t)
	account := billing.Account{ID: "submission-zero-sibling", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	firstCall, firstExposure := phase10SubmissionCall(t, store, account.ID, "submission-zero-sibling", "a-zero-sibling-1")
	firstValuation := phase10SubmissionValuation(t, firstCall, 20, 0, "tariff-v1", "policy-v1")
	firstResult := billing.CallRatingResult{
		CallID: firstCall.CallID, CustomerCharge: billing.Money{Nano: 20, Currency: "USD"},
		Fingerprint: firstValuation.Fingerprint(), CustomerValuation: firstValuation,
	}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: firstCall, Exposure: firstExposure, Result: firstResult}); err != nil {
		t.Fatalf("first submission-fee-only settlement: %v", err)
	}

	secondCall, secondExposure := phase10SubmissionCall(t, store, account.ID, "submission-zero-sibling", "a-zero-sibling-2")
	secondValuation := phase10SubmissionValuation(t, secondCall, 20, 0, "tariff-v1", "policy-v1")
	secondResult := billing.CallRatingResult{
		CallID: secondCall.CallID, CustomerCharge: billing.Money{Nano: 20, Currency: "USD"},
		Fingerprint: secondValuation.Fingerprint(), CustomerValuation: secondValuation,
	}
	second, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: secondCall, Exposure: secondExposure, Result: secondResult})
	if err != nil {
		t.Fatalf("zero-priced sibling settlement: %v", err)
	}
	if second.Replayed || second.Customer.Transaction.ID != "" {
		t.Fatalf("zero-priced sibling settlement = %+v, want fresh no-transaction outcome", second)
	}
	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 980 {
		t.Fatalf("zero-priced sibling balance = %d, want 980 (submission fee once)", got.BalanceNano)
	}
	transactions, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(transactions) != 1 {
		t.Fatalf("zero-priced sibling journal count = %d, want one owner transaction", len(transactions))
	}
}

func TestSQLiteV2SubmissionFeeOverrunReplayPreservesDurableOwnerState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := f3NewStore(t, "submission-v2-overrun-replay")
	accountID := "submission-v2-overrun-replay"
	f3SetupAccount(t, store, accountID, 1_000)
	f3ActivateEmpty(t, store)
	callID := f3MustCallID(t)
	call := testIndependentCallUsageFor(callID, []string{"b-submission-v2"})
	call.AccountID = accountID
	call.SubmissionID = "submission-v2-overrun"
	exposure, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(), Max: billing.Money{Nano: 100, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	}, billing.PostingOwnerV2)
	if err != nil {
		t.Fatalf("V2 exposure admission: %v", err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, call, billing.PostingOwnerV2); err != nil {
		t.Fatalf("V2 call closure: %v", err)
	}
	if err := store.AppendCallLegUsageWithOwner(ctx, testIndependentCallLegFor(callID, "b-submission-v2"), billing.PostingOwnerV2); err != nil {
		t.Fatalf("V2 call leg: %v", err)
	}
	valuation := phase10V2SubmissionValuation(t, call, 20, 100)
	result := billing.CallRatingResult{
		CallID: callID, CustomerCharge: billing.Money{Nano: 120, Currency: "USD"},
		Fingerprint: valuation.Fingerprint(), CustomerValuation: valuation,
	}
	input := billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result, PostingOwner: billing.PostingOwnerV2}
	first, err := store.ApplyCallBillingResult(ctx, input)
	if err != nil {
		t.Fatalf("first V2 overrun settlement: %v", err)
	}
	if first.Replayed || !first.Breached || first.OverrunNano != 20 || first.Customer.Transaction.ID == "" {
		t.Fatalf("first V2 settlement = %+v, want fresh 120-nano posting with 20-nano overrun", first)
	}
	sourceKey, err := billing.CustomerSettlementSourceKey(accountID, callID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, found, err := loadOperationSnapshot(ctx, store.db, accountID, "customer_call_settlement", callID.String())
	if err != nil || !found {
		t.Fatalf("load V2 settlement snapshot: found=%v err=%v", found, err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, sourceKey)
	if err != nil {
		t.Fatalf("load V2 settlement pin: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV2 || !pin.IsCompleted() || pin.CompletionTransactionID != first.Customer.Transaction.ID {
		t.Fatalf("first V2 settlement pin = %+v, want completed owner/transaction", pin)
	}
	firstAccount, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	firstTransactions, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if firstAccount.BalanceNano != 880 || len(firstTransactions) != 1 {
		t.Fatalf("first V2 durable effects = balance %d transactions %d, want 880 and 1", firstAccount.BalanceNano, len(firstTransactions))
	}

	replayed, err := store.ApplyCallBillingResult(ctx, input)
	if err != nil {
		t.Fatalf("exact V2 overrun replay after claim: %v", err)
	}
	if !replayed.Replayed || !replayed.Breached || replayed.OverrunNano != 20 {
		t.Fatalf("V2 replay settlement = %+v, want identical disposition", replayed)
	}
	afterSnapshot, found, err := loadOperationSnapshot(ctx, store.db, accountID, "customer_call_settlement", callID.String())
	if err != nil || !found || afterSnapshot.Fingerprint != snapshot.Fingerprint {
		t.Fatalf("V2 replay fingerprint = %q, want %q (found=%v err=%v)", afterSnapshot.Fingerprint, snapshot.Fingerprint, found, err)
	}
	afterAccount, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	afterTransactions, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	afterPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, sourceKey)
	if err != nil {
		t.Fatal(err)
	}
	if afterAccount.BalanceNano != firstAccount.BalanceNano || len(afterTransactions) != 1 || afterTransactions[0].ID != firstTransactions[0].ID {
		t.Fatalf("V2 replay changed durable money: before balance/tx=%d/%s after=%d/%s", firstAccount.BalanceNano, firstTransactions[0].ID, afterAccount.BalanceNano, afterTransactions[0].ID)
	}
	if afterPin != pin {
		t.Fatalf("V2 replay changed pin: before=%+v after=%+v", pin, afterPin)
	}
}

func TestSQLiteSubmissionFeeOverrunReplayAfterConnectionRecreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate", filepath.ToSlash(filepath.Join(t.TempDir(), "submission-replay.db")))
	open := func() *DurableStore {
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(16)
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			_ = sqlDB.Close()
			t.Fatal(err)
		}
		seedTestSchemaIfEmpty(t, bunDB)
		store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "submission-reopen"})
		if err != nil {
			_ = bunDB.Close()
			t.Fatal(err)
		}
		return store
	}
	store := open()
	account := billing.Account{ID: "submission-reopen", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	call, exposure := phase10SubmissionCallWithMax(t, store, account.ID, "submission-reopen", "a-reopen", 100)
	valuation := phase10SubmissionValuation(t, call, 20, 100, "tariff-v1", "policy-v1")
	result := billing.CallRatingResult{
		CallID: call.CallID, CustomerCharge: billing.Money{Nano: 120, Currency: "USD"},
		Fingerprint: valuation.Fingerprint(), CustomerValuation: valuation,
	}
	first, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result})
	if err != nil {
		t.Fatalf("first settlement before connection recreation: %v", err)
	}
	if !first.Breached || first.OverrunNano != 20 || first.Customer.Transaction.ID == "" {
		t.Fatalf("first settlement = %+v, want posted overrun", first)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first connection: %v", err)
	}
	reopened := open()
	t.Cleanup(func() { _ = reopened.Close() })
	replayed, err := reopened.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result})
	if err != nil {
		t.Fatalf("lost-ack replay after connection recreation: %v", err)
	}
	if !replayed.Replayed || !replayed.Breached || replayed.OverrunNano != 20 {
		t.Fatalf("recreated-connection replay = %+v, want identical disposition", replayed)
	}
	got, err := reopened.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 880 {
		t.Fatalf("recreated-connection balance = %d, want 880", got.BalanceNano)
	}
	transactions, err := reopened.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(transactions) != 1 || transactions[0].ID != first.Customer.Transaction.ID {
		t.Fatalf("recreated-connection transactions = %+v, want original one transaction %q", transactions, first.Customer.Transaction.ID)
	}
}

// TestSQLiteV2SubmissionFeeBelowExposureOwnerReplayStable covers the R2
// counterexample's below-exposure twin: the fee-owning V2 call settles under
// its admitted maximum, then its own exact replay must not deduct the
// submission fee it created. The posting amount, journal transaction, pin,
// fingerprint, balance, and breach disposition stay stable.
func TestSQLiteV2SubmissionFeeBelowExposureOwnerReplayStable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := f3NewStore(t, "submission-v2-below")
	accountID := "submission-v2-below"
	f3SetupAccount(t, store, accountID, 1_000)
	f3ActivateEmpty(t, store)
	call, exposure := phase10V2SubmissionCall(t, store, accountID, "submission-v2-below", "b-below", 200)
	result := phase10V2SubmissionResult(t, call, 20, 100)
	input := billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result, PostingOwner: billing.PostingOwnerV2}
	first, err := store.ApplyCallBillingResult(ctx, input)
	if err != nil {
		t.Fatalf("first below-exposure V2 settlement: %v", err)
	}
	if first.Replayed || first.Breached || first.OverrunNano != 0 || first.Customer.Transaction.ID == "" {
		t.Fatalf("first below-exposure settlement = %+v, want fresh 120-nano posting without breach", first)
	}
	sourceKey, err := billing.CustomerSettlementSourceKey(accountID, call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	firstPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, sourceKey)
	if err != nil {
		t.Fatalf("load below-exposure pin: %v", err)
	}
	if firstPin.Owner != billing.PostingOwnerV2 || !firstPin.IsCompleted() || firstPin.CompletionTransactionID != first.Customer.Transaction.ID {
		t.Fatalf("below-exposure pin = %+v, want completed V2 owner tx %q", firstPin, first.Customer.Transaction.ID)
	}
	firstSnapshot, found, err := loadOperationSnapshot(ctx, store.db, accountID, "customer_call_settlement", call.CallID.String())
	if err != nil || !found {
		t.Fatalf("load below-exposure snapshot: found=%v err=%v", found, err)
	}
	firstAccount, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	firstTransactions, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if firstAccount.BalanceNano != 880 || len(firstTransactions) != 1 {
		t.Fatalf("first below-exposure effects = balance %d transactions %d, want 880 and 1", firstAccount.BalanceNano, len(firstTransactions))
	}

	replayed, err := store.ApplyCallBillingResult(ctx, input)
	if err != nil {
		t.Fatalf("exact below-exposure owner replay: %v", err)
	}
	if !replayed.Replayed || replayed.Breached || replayed.OverrunNano != 0 {
		t.Fatalf("below-exposure replay = %+v, want unchanged non-breached replay", replayed)
	}
	afterSnapshot, found, err := loadOperationSnapshot(ctx, store.db, accountID, "customer_call_settlement", call.CallID.String())
	if err != nil || !found || afterSnapshot.Fingerprint != firstSnapshot.Fingerprint {
		t.Fatalf("below-exposure replay fingerprint = %q, want %q (found=%v err=%v)", afterSnapshot.Fingerprint, firstSnapshot.Fingerprint, found, err)
	}
	afterPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, sourceKey)
	if err != nil {
		t.Fatal(err)
	}
	afterAccount, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	afterTransactions, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if afterPin != firstPin {
		t.Fatalf("below-exposure replay changed pin: before=%+v after=%+v", firstPin, afterPin)
	}
	if afterAccount.BalanceNano != 880 || len(afterTransactions) != 1 || afterTransactions[0].ID != first.Customer.Transaction.ID {
		t.Fatalf("below-exposure replay changed durable money: balance=%d transactions=%+v", afterAccount.BalanceNano, afterTransactions)
	}
	phase10AssertExposureState(t, store, call.CallID, exposure.Fingerprint)
}

// TestSQLiteV2SubmissionFeeOnlyZeroPricedOwnerReplayAfterZeroEffectiveSibling is
// the direct R2 acceptance fixture: a legitimate V2 charge is the submission
// fee only (a zero-priced inference component), the owner posts it and creates
// the immutable claim. A sibling call under the same submission then settles as
// a zero-effective sibling (its customer charge minus the already-claimed fee,
// with no new journal). The owner's own exact replay must still return the
// original posting identity: stable operation fingerprint, expected transaction
// ID, completed pin, balance, journal, and one claim. Before the narrow
// call_settlement fix the owner replay deducts its own claim, computes an empty
// expected transaction ID, and conflicts with the completed owner pin.
func TestSQLiteV2SubmissionFeeOnlyZeroPricedOwnerReplayAfterZeroEffectiveSibling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := f3NewStore(t, "submission-v2-fee-only")
	accountID := "submission-v2-fee-only"
	submissionID := "submission-v2-fee-only"
	f3SetupAccount(t, store, accountID, 1_000)
	f3ActivateEmpty(t, store)

	owner, ownerExposure := phase10V2SubmissionCall(t, store, accountID, submissionID, "b-owner", 100)
	ownerResult := phase10V2SubmissionResult(t, owner, 20, 0)
	ownerInput := billing.ApplyCallBillingInput{Call: owner, Exposure: ownerExposure, Result: ownerResult, PostingOwner: billing.PostingOwnerV2}
	ownerSettlement, err := store.ApplyCallBillingResult(ctx, ownerInput)
	if err != nil {
		t.Fatalf("owner fee-only V2 settlement: %v", err)
	}
	if ownerSettlement.Replayed || ownerSettlement.Breached || ownerSettlement.Customer.Transaction.ID == "" {
		t.Fatalf("owner fee-only settlement = %+v, want fresh 20-nano posting of the submission fee", ownerSettlement)
	}
	ownerSourceKey, err := billing.CustomerSettlementSourceKey(accountID, owner.CallID)
	if err != nil {
		t.Fatal(err)
	}
	ownerPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, ownerSourceKey)
	if err != nil {
		t.Fatalf("load owner pin: %v", err)
	}
	if ownerPin.Owner != billing.PostingOwnerV2 || !ownerPin.IsCompleted() || ownerPin.CompletionTransactionID != ownerSettlement.Customer.Transaction.ID {
		t.Fatalf("owner pin = %+v, want completed V2 owner tx %q", ownerPin, ownerSettlement.Customer.Transaction.ID)
	}
	if sourceCallID, amount := phase10SubmissionClaimRow(t, store, accountID, submissionID); sourceCallID != owner.CallID.String() || amount != 20 {
		t.Fatalf("submission claim = (%q, %d), want owner %q amount 20", sourceCallID, amount, owner.CallID)
	}

	sibling, siblingExposure := phase10V2SubmissionCall(t, store, accountID, submissionID, "b-sibling", 100)
	siblingResult := phase10V2SubmissionResult(t, sibling, 20, 0)
	siblingInput := billing.ApplyCallBillingInput{Call: sibling, Exposure: siblingExposure, Result: siblingResult, PostingOwner: billing.PostingOwnerV2}
	siblingSettlement, err := store.ApplyCallBillingResult(ctx, siblingInput)
	if err != nil {
		t.Fatalf("zero-effective sibling settlement: %v", err)
	}
	if siblingSettlement.Replayed || siblingSettlement.Breached || siblingSettlement.Customer.Transaction.ID != "" {
		t.Fatalf("zero-effective sibling = %+v, want fresh no-transaction sibling", siblingSettlement)
	}
	if sourceCallID, amount := phase10SubmissionClaimRow(t, store, accountID, submissionID); sourceCallID != owner.CallID.String() || amount != 20 {
		t.Fatalf("submission claim after sibling = (%q, %d), want unchanged owner %q amount 20", sourceCallID, amount, owner.CallID)
	}
	if count := phase10SubmissionClaimCount(t, store, accountID, submissionID); count != 1 {
		t.Fatalf("submission claim count after sibling = %d, want one", count)
	}
	siblingBalance, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if siblingBalance.BalanceNano != 980 {
		t.Fatalf("balance after zero-effective sibling = %d, want 980", siblingBalance.BalanceNano)
	}

	siblingReplay, err := store.ApplyCallBillingResult(ctx, siblingInput)
	if err != nil {
		t.Fatalf("zero-charge sibling replay: %v", err)
	}
	if !siblingReplay.Replayed || siblingReplay.Breached || siblingReplay.Customer.Transaction.ID != "" {
		t.Fatalf("zero-charge sibling replay = %+v, want unchanged replay without transaction", siblingReplay)
	}
	siblingSourceKey, err := billing.CustomerSettlementSourceKey(accountID, sibling.CallID)
	if err != nil {
		t.Fatal(err)
	}
	siblingPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, siblingSourceKey)
	if err != nil {
		t.Fatalf("load sibling pin: %v", err)
	}
	if !siblingPin.IsCompleted() || siblingPin.CompletionTransactionID != "" {
		t.Fatalf("sibling pin = %+v, want completed zero-transaction pin", siblingPin)
	}

	ownerReplay, err := store.ApplyCallBillingResult(ctx, ownerInput)
	if err != nil {
		t.Fatalf("owner exact replay after zero-effective sibling: %v", err)
	}
	if !ownerReplay.Replayed || ownerReplay.Breached || ownerReplay.OverrunNano != 0 {
		t.Fatalf("owner replay = %+v, want unchanged non-breached replay", ownerReplay)
	}
	afterPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, ownerSourceKey)
	if err != nil {
		t.Fatal(err)
	}
	if afterPin != ownerPin {
		t.Fatalf("owner replay changed pin: before=%+v after=%+v", ownerPin, afterPin)
	}
	if sourceCallID, amount := phase10SubmissionClaimRow(t, store, accountID, submissionID); sourceCallID != owner.CallID.String() || amount != 20 {
		t.Fatalf("submission claim after owner replay = (%q, %d), want unchanged owner %q amount 20", sourceCallID, amount, owner.CallID)
	}
	afterAccount, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	transactions, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if afterAccount.BalanceNano != 980 || len(transactions) != 1 || transactions[0].ID != ownerSettlement.Customer.Transaction.ID {
		t.Fatalf("owner replay durable effects = balance %d transactions %+v, want 980 and the original owner transaction %q", afterAccount.BalanceNano, transactions, ownerSettlement.Customer.Transaction.ID)
	}
	phase10AssertExposureState(t, store, owner.CallID, ownerExposure.Fingerprint)
	phase10AssertExposureState(t, store, sibling.CallID, siblingExposure.Fingerprint)
}

// TestSQLiteV2SubmissionFeeChangedValuationConflictsWithoutEffects proves the
// R2 fix does not weaken conflict detection: a different but structurally valid
// V2 valuation for the fee-owning call (recomputed fingerprint, different
// charge) must fail with ErrOperationConflict and leave money, exposure, and
// pin history untouched.
func TestSQLiteV2SubmissionFeeChangedValuationConflictsWithoutEffects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := f3NewStore(t, "submission-v2-changed")
	accountID := "submission-v2-changed"
	submissionID := "submission-v2-changed"
	f3SetupAccount(t, store, accountID, 1_000)
	f3ActivateEmpty(t, store)
	call, exposure := phase10V2SubmissionCall(t, store, accountID, submissionID, "b-changed", 100)
	result := phase10V2SubmissionResult(t, call, 20, 0)
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result, PostingOwner: billing.PostingOwnerV2}); err != nil {
		t.Fatalf("first fee-only V2 settlement: %v", err)
	}
	sourceKey, err := billing.CustomerSettlementSourceKey(accountID, call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	beforePin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, sourceKey)
	if err != nil {
		t.Fatal(err)
	}
	beforeAccount, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	beforeTransactions, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	beforeSnapshot, found, err := loadOperationSnapshot(ctx, store.db, accountID, "customer_call_settlement", call.CallID.String())
	if err != nil || !found {
		t.Fatalf("load pre-conflict snapshot: found=%v err=%v", found, err)
	}

	changed := phase10V2SubmissionResult(t, call, 20, 5)
	if changed.Fingerprint == result.Fingerprint {
		t.Fatal("changed valuation must carry a recomputed fingerprint")
	}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: changed, PostingOwner: billing.PostingOwnerV2}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("changed V2 valuation = %v, want ErrOperationConflict", err)
	}
	afterPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, sourceKey)
	if err != nil {
		t.Fatal(err)
	}
	afterAccount, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	afterTransactions, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	afterSnapshot, found, err := loadOperationSnapshot(ctx, store.db, accountID, "customer_call_settlement", call.CallID.String())
	if err != nil || !found {
		t.Fatalf("load post-conflict snapshot: found=%v err=%v", found, err)
	}
	if afterPin != beforePin {
		t.Fatalf("changed valuation mutated pin: before=%+v after=%+v", beforePin, afterPin)
	}
	if afterAccount.BalanceNano != beforeAccount.BalanceNano || len(afterTransactions) != len(beforeTransactions) {
		t.Fatalf("changed valuation mutated money: balance %d->%d transactions %d->%d", beforeAccount.BalanceNano, afterAccount.BalanceNano, len(beforeTransactions), len(afterTransactions))
	}
	if afterSnapshot.Fingerprint != beforeSnapshot.Fingerprint {
		t.Fatalf("changed valuation mutated settlement fingerprint: %q -> %q", beforeSnapshot.Fingerprint, afterSnapshot.Fingerprint)
	}
	phase10AssertExposureState(t, store, call.CallID, exposure.Fingerprint)
}

// TestSQLiteV2SubmissionFeeOverrunReplayAfterConnectionRecreation is the R2
// acknowledgement-lost variant for V2: the overrun owner result is committed,
// the SQL/store handles are recreated, and the exact replay must reconstruct
// the same posting, transaction, pin, balance, journal, and claim from durable
// state alone.
func TestSQLiteV2SubmissionFeeOverrunReplayAfterConnectionRecreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate", filepath.ToSlash(filepath.Join(t.TempDir(), "submission-v2-replay.db")))
	open := func() *DurableStore {
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(16)
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			_ = sqlDB.Close()
			t.Fatal(err)
		}
		seedTestSchemaIfEmpty(t, bunDB)
		store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "submission-v2-reopen"})
		if err != nil {
			_ = bunDB.Close()
			t.Fatal(err)
		}
		return store
	}
	store := open()
	accountID := "submission-v2-reopen"
	f3SetupAccount(t, store, accountID, 1_000)
	f3ActivateEmpty(t, store)
	call, exposure := phase10V2SubmissionCall(t, store, accountID, "submission-v2-reopen", "b-reopen", 100)
	result := phase10V2SubmissionResult(t, call, 20, 100)
	input := billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result, PostingOwner: billing.PostingOwnerV2}
	first, err := store.ApplyCallBillingResult(ctx, input)
	if err != nil {
		t.Fatalf("first V2 settlement before connection recreation: %v", err)
	}
	if first.Replayed || !first.Breached || first.OverrunNano != 20 || first.Customer.Transaction.ID == "" {
		t.Fatalf("first V2 settlement = %+v, want fresh 120-nano posting with 20-nano overrun", first)
	}
	sourceKey, err := billing.CustomerSettlementSourceKey(accountID, call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	firstPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, sourceKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first connection: %v", err)
	}
	reopened := open()
	t.Cleanup(func() { _ = reopened.Close() })
	replayed, err := reopened.ApplyCallBillingResult(ctx, input)
	if err != nil {
		t.Fatalf("lost-ack V2 replay after connection recreation: %v", err)
	}
	if !replayed.Replayed || !replayed.Breached || replayed.OverrunNano != 20 {
		t.Fatalf("recreated-connection V2 replay = %+v, want identical disposition", replayed)
	}
	afterPin, err := reopened.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, sourceKey)
	if err != nil {
		t.Fatal(err)
	}
	if afterPin != firstPin {
		t.Fatalf("recreated-connection pin = %+v, want %+v", afterPin, firstPin)
	}
	afterAccount, err := reopened.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	transactions, err := reopened.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if afterAccount.BalanceNano != 880 || len(transactions) != 1 || transactions[0].ID != first.Customer.Transaction.ID {
		t.Fatalf("recreated-connection effects = balance %d transactions %+v, want 880 and original tx %q", afterAccount.BalanceNano, transactions, first.Customer.Transaction.ID)
	}
	if sourceCallID, amount := phase10SubmissionClaimRow(t, reopened, accountID, "submission-v2-reopen"); sourceCallID != call.CallID.String() || amount != 20 {
		t.Fatalf("recreated-connection claim = (%q, %d), want owner %q amount 20", sourceCallID, amount, call.CallID)
	}
	phase10AssertExposureState(t, reopened, call.CallID, exposure.Fingerprint)
}

// TestSQLiteV2SubmissionFeeConcurrentFirstClaimReplayStable is the SQLite half
// of the R2 concurrent-first-claim acceptance that the Astra review found
// missing. Two file-backed SQLite handles admit and close two valid V2 calls
// sharing one trusted submission, then settle them at a shared start barrier so
// the first-claim attempts genuinely compete. The durable immutable claim
// elects exactly one owner; the owner keeps its full 120 charge (breaching the
// 100 exposure by 20) and the sibling deducts the claimed fee to settle at 100.
// Both exact inputs are then replayed on fresh handles and must change nothing.
// The pre-fix production behavior fails this test at the owner replay.
func TestSQLiteV2SubmissionFeeConcurrentFirstClaimReplayStable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for iteration := 0; iteration < 5; iteration++ {
		storeID := fmt.Sprintf("submission-v2-concurrent-%d", iteration)
		dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate", filepath.ToSlash(filepath.Join(t.TempDir(), storeID+".db")))
		open := func() [2]*DurableStore {
			stores := phase10OpenSQLiteFileStores(t, dsn, storeID, 2)
			return [2]*DurableStore{stores[0], stores[1]}
		}
		settlers := open()
		f3SetupAccount(t, settlers[0], storeID, 1_000)
		f3ActivateEmpty(t, settlers[0])
		inputs := phase10V2ConcurrentSubmissionInputs(t, settlers[0], storeID, storeID)
		phase10AssertV2ConcurrentFirstClaimAndReplay(t, ctx, storeID, storeID, settlers, open, inputs)
	}
}

func TestSQLiteSubmissionFeeClaimIsNotPersistedWhenSettlementReconciles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteTestStore(t)
	account := billing.Account{ID: "submission-rollback", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	failedCall, failedExposure := phase10SubmissionCallWithMax(t, store, account.ID, "submission-rollback", "a-rollback-failed", 10)
	// The failed charge exceeds spendable, so settlement reconciles under the
	// existing account policy and the whole transaction (including the
	// submission fee claim) rolls back.
	failedResult := billing.CallRatingResult{CallID: failedCall.CallID, CustomerCharge: billing.Money{Nano: 150, Currency: "USD"}, Fingerprint: "submission-rollback-failed", CustomerValuation: phase10SubmissionValuation(t, failedCall, 5, 10, "tariff-v1", "policy-v1")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: failedCall, Exposure: failedExposure, Result: failedResult}); !errors.Is(err, billing.ErrSettlementReconcileRequired) {
		t.Fatalf("failed settlement = %v, want reconciliation", err)
	}
	if _, err := store.db.NewRaw(`UPDATE billing_accounts SET state = 'ready' WHERE account_id = ?`, account.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	validCall, validExposure := phase10SubmissionCallWithMax(t, store, account.ID, "submission-rollback", "a-rollback-valid", 20)
	validResult := billing.CallRatingResult{CallID: validCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-rollback-valid", CustomerValuation: phase10SubmissionValuation(t, validCall, 5, 10, "tariff-v1", "policy-v1")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: validCall, Exposure: validExposure, Result: validResult}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 85 {
		t.Fatalf("balance after retry following rollback = %d, want 85", got.BalanceNano)
	}
	var claims int
	if err := store.db.NewRaw(`SELECT COUNT(*) FROM billing_submission_fee_claims WHERE store_id = ? AND account_id = ? AND submission_id = ?`, "test", account.ID, "submission-rollback").Scan(ctx, &claims); err != nil {
		t.Fatal(err)
	}
	if claims != 1 {
		t.Fatalf("rollback claim count = %d, want one after successful retry", claims)
	}
}

func TestSQLiteSubmissionFeeClaimIsolatedByStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	left, right := newSQLiteTestStore(t), newSQLiteTestStore(t)
	account := billing.Account{ID: "submission-store-isolation", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	for _, store := range []*DurableStore{left, right} {
		if err := store.CreateAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		call, exposure := phase10SubmissionCall(t, store, account.ID, "submission-store", "a-store")
		result := billing.CallRatingResult{CallID: call.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-store-result", CustomerValuation: phase10SubmissionValuation(t, call, 5, 10, "tariff-v1", "policy-v1")}
		if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result}); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetAccount(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.BalanceNano != 85 {
			t.Fatalf("isolated store balance = %d, want 85", got.BalanceNano)
		}
	}
}

func TestSQLiteSubmissionFeeSettlementRejectsFrozenTariffOrPolicyConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteTestStore(t)
	account := billing.Account{ID: "submission-context-conflict", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	firstCall, firstExposure := phase10SubmissionCall(t, store, account.ID, "submission-context", "a-context-1")
	firstResult := billing.CallRatingResult{CallID: firstCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-context-1", CustomerValuation: phase10SubmissionValuation(t, firstCall, 5, 10, "tariff-v1", "policy-v1")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: firstCall, Exposure: firstExposure, Result: firstResult}); err != nil {
		t.Fatal(err)
	}

	secondCall, secondExposure := phase10SubmissionCall(t, store, account.ID, "submission-context", "a-context-2")
	secondResult := billing.CallRatingResult{CallID: secondCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-context-2", CustomerValuation: phase10SubmissionValuation(t, secondCall, 5, 10, "tariff-v2", "policy-v1")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: secondCall, Exposure: secondExposure, Result: secondResult}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("tariff conflict = %v, want ErrOperationConflict", err)
	}
	thirdCall, thirdExposure := phase10SubmissionCall(t, store, account.ID, "submission-context-policy", "a-context-3")
	thirdResult := billing.CallRatingResult{CallID: thirdCall.CallID, CustomerCharge: billing.Money{Nano: 15, Currency: "USD"}, Fingerprint: "submission-context-3", CustomerValuation: phase10SubmissionValuation(t, thirdCall, 5, 10, "tariff-v1", "policy-v2")}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: thirdCall, Exposure: thirdExposure, Result: thirdResult}); err != nil {
		t.Fatalf("new submission with policy context: %v", err)
	}

	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 970 {
		t.Fatalf("balance after context tests = %d, want 970 (first + distinct submission)", got.BalanceNano)
	}
}

func phase10SubmissionCall(t *testing.T, store *DurableStore, accountID, submissionID, aLegID string) (billing.CallUsageRecord, billing.CallExposure) {
	t.Helper()
	return phase10SubmissionCallWithMax(t, store, accountID, submissionID, aLegID, 100)
}

// phase10V2SubmissionCall admits and closes one V2-owned call through the
// production owner-aware admission/terminal APIs, with a trusted submission
// identity. It never preloads V1 state or inserts settlement rows directly.
func phase10V2SubmissionCall(t *testing.T, store *DurableStore, accountID, submissionID, bLegID string, maxNano int64) (billing.CallUsageRecord, billing.CallExposure) {
	t.Helper()
	ctx := context.Background()
	callID := f3MustCallID(t)
	call := testIndependentCallUsageFor(callID, []string{bLegID})
	call.AccountID = accountID
	call.SubmissionID = submissionID
	exposure, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(), Max: billing.Money{Nano: maxNano, Currency: "USD"}, PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	}, billing.PostingOwnerV2)
	if err != nil {
		t.Fatalf("V2 submission exposure admission: %v", err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, call, billing.PostingOwnerV2); err != nil {
		t.Fatalf("V2 submission call closure: %v", err)
	}
	if err := store.AppendCallLegUsageWithOwner(ctx, testIndependentCallLegFor(callID, bLegID), billing.PostingOwnerV2); err != nil {
		t.Fatalf("V2 submission call leg: %v", err)
	}
	return call, exposure
}

// phase10V2SubmissionResult binds a V2 customer valuation (one zero-or-more
// priced inference component plus a submission fee) to the actual call and
// returns the settlement-ready result whose fingerprint is the valuation
// fingerprint.
func phase10V2SubmissionResult(t *testing.T, call billing.CallUsageRecord, submissionFee, callFee int64) billing.CallRatingResult {
	t.Helper()
	valuation := phase10V2SubmissionValuation(t, call, submissionFee, callFee)
	return billing.CallRatingResult{
		CallID: call.CallID, CustomerCharge: billing.Money{Nano: submissionFee + callFee, Currency: "USD"},
		Fingerprint: valuation.Fingerprint(), CustomerValuation: valuation,
	}
}

func phase10SubmissionClaimRow(t *testing.T, store *DurableStore, accountID, submissionID string) (string, int64) {
	t.Helper()
	var sourceCallID string
	var amountNano int64
	if err := store.db.NewRaw(`SELECT source_call_id, amount_nano FROM billing_submission_fee_claims WHERE store_id = ? AND account_id = ? AND submission_id = ?`, store.StoreID(), accountID, submissionID).Scan(context.Background(), &sourceCallID, &amountNano); err != nil {
		t.Fatalf("load submission fee claim: %v", err)
	}
	return sourceCallID, amountNano
}

func phase10SubmissionClaimCount(t *testing.T, store *DurableStore, accountID, submissionID string) int {
	t.Helper()
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(*) FROM billing_submission_fee_claims WHERE store_id = ? AND account_id = ? AND submission_id = ?`, store.StoreID(), accountID, submissionID).Scan(context.Background(), &count); err != nil {
		t.Fatalf("count submission fee claims: %v", err)
	}
	return count
}

// phase10AssertExposureState proves replay/conflict paths left the call exposure
// closed and fingerprint-identical to admission.
func phase10AssertExposureState(t *testing.T, store *DurableStore, callID billing.BillingCallID, wantFingerprint string) {
	t.Helper()
	exposure, err := store.GetCallExposure(context.Background(), callID)
	if err != nil {
		t.Fatalf("load call exposure %s: %v", callID, err)
	}
	if exposure.IsOpen() {
		t.Fatalf("exposure %s is open, want closed", callID)
	}
	if exposure.Fingerprint != wantFingerprint {
		t.Fatalf("exposure %s fingerprint = %q, want %q", callID, exposure.Fingerprint, wantFingerprint)
	}
}

func phase10SubmissionCallWithMax(t *testing.T, store *DurableStore, accountID, submissionID, aLegID string, maxNano int64) (billing.CallUsageRecord, billing.CallExposure) {
	t.Helper()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID, ALegID: aLegID,
		SessionID: "session-" + aLegID, SubmissionID: submissionID, StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(), Outcome: billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
	}
	if err := store.AppendCallUsage(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(context.Background(), billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(), Max: billing.Money{Nano: maxNano, Currency: "USD"}, PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	return call, exposure
}

func phase10SubmissionValuation(t *testing.T, call billing.CallUsageRecord, submissionFee, callFee int64, tariffVersion, policyVersion string) economics.Valuation {
	t.Helper()
	ref := metering.ObservationRef{StoreID: "test", ObservationID: "observation-" + call.CallID.String(), Revision: 1, PayloadHash: "payload-" + call.CallID.String()}
	inputHash, err := economics.CanonicalInputSetHash(economics.BasisCustomerPolicy, []metering.ObservationRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	tariffHash := phase10SubmissionHash("tariff", tariffVersion)
	policyHash := phase10SubmissionHash("policy", policyVersion)
	qualifierHash := phase10SubmissionHash("qualifier", tariffVersion+"/"+policyVersion)
	lines := []economics.LineItem{
		phase10SubmissionLine(t, "fixed:submission-fee", "submission-fee", economics.FixedFeeScopeSubmission, submissionFee, tariffVersion),
		phase10SubmissionLine(t, "fixed:call-fee", "call-fee", economics.FixedFeeScopeCall, callFee, tariffVersion),
	}
	total := submissionFee + callFee
	totalDecimal := phase10SubmissionDecimal(t, total)
	return economics.Valuation{
		ID: "valuation-" + call.CallID.String(), Version: economics.ValuationVersionV2, Perspective: metering.PerspectiveCustomer, Basis: economics.BasisCustomerPolicy,
		Subject: metering.SubjectRef{Kind: metering.SubjectBillingCall, StoreID: "test", BillingCallID: call.CallID.String()}, Scope: "call:" + call.CallID.String(),
		InputObservations: []metering.ObservationRef{ref}, InputSetHash: inputHash,
		Rater:                economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rater", Version: tariffVersion}, RaterID: "reference"},
		RaterContent:         &economics.SnapshotContentRef{ContentRef: "rater://" + tariffVersion, ContentHash: tariffHash},
		Tariff:               economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff", Version: tariffVersion}, RaterID: "reference"},
		TariffContent:        &economics.SnapshotContentRef{ContentRef: "tariff://" + tariffVersion, ContentHash: tariffHash},
		Policy:               economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy", Version: policyVersion}, PolicyID: "policy"},
		PolicyContent:        &economics.SnapshotContentRef{ContentRef: "policy://" + policyVersion, ContentHash: policyHash},
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "qualifier://" + tariffVersion + "/" + policyVersion, ContentHash: qualifierHash},
		Lines:                lines, Totals: []economics.CurrencyTotal{{Currency: "USD", Amount: totalDecimal, RoundedAmount: economics.Money{NanoUnits: total, Currency: "USD", Present: true}}},
		Completeness: economics.CompletenessComplete, CreatedAt: time.Unix(101, 0).UTC(),
	}
}

func phase10V2SubmissionValuation(t *testing.T, call billing.CallUsageRecord, submissionFee, callFee int64) economics.Valuation {
	t.Helper()
	charge := billing.Money{Nano: submissionFee + callFee, Currency: "USD"}
	valuation := f3BoundComponentValuation(t, call, charge)
	component := valuation.Lines[0].Clone()
	amount := f3BoundDecimal(t, callFee)
	rounded := economics.Money{NanoUnits: callFee, Currency: "USD", Present: true}
	component.Amount = &amount
	component.UnitPrice = &amount
	component.RoundedAmount = &rounded
	valuation.Lines = []economics.LineItem{component, phase10SubmissionLine(t, "fixed:submission-fee", "submission-fee", economics.FixedFeeScopeSubmission, submissionFee, "v1")}
	if err := valuation.Validate(); err != nil {
		t.Fatalf("V2 submission valuation must validate: %v", err)
	}
	return valuation
}

func phase10SubmissionLine(t *testing.T, id, ruleID string, scope economics.FixedFeeScope, nano int64, version string) economics.LineItem {
	t.Helper()
	amount := phase10SubmissionDecimal(t, nano)
	return economics.LineItem{
		ID: id, RuleID: ruleID, ItemID: ruleID, Unit: metering.UnitCount,
		FixedFee: &economics.FixedFeeIdentity{ID: ruleID, Scope: scope, Version: version}, Amount: amount,
		RoundedAmount: &economics.Money{NanoUnits: nano, Currency: "USD", Present: true}, RoundingScope: economics.RoundingScopeLine,
		RoundingPolicy: economics.RoundingHalfAwayFromZero, Status: economics.RatingLineRated, ChargeKind: "commercial_fee",
	}
}

func phase10SubmissionDecimal(t *testing.T, nano int64) *metering.Decimal {
	t.Helper()
	whole := nano / 1_000_000_000
	frac := nano % 1_000_000_000
	value, err := metering.ParseDecimal(fmt.Sprintf("%d.%09d", whole, frac))
	if err != nil {
		t.Fatal(err)
	}
	return &value
}

func phase10SubmissionHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// phase10OpenSQLiteFileStores opens count independent file-backed SQLite store
// handles over one database file. Independent connection pools are required so
// two concurrent first-claim settlements compete at the database boundary
// instead of sharing a single connection.
func phase10OpenSQLiteFileStores(t *testing.T, dsn, storeID string, count int) []*DurableStore {
	t.Helper()
	stores := make([]*DurableStore, count)
	for i := range stores {
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatalf("open sqlite handle %d: %v", i, err)
		}
		sqlDB.SetMaxOpenConns(16)
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			_ = sqlDB.Close()
			t.Fatalf("wrap sqlite handle %d: %v", i, err)
		}
		seedTestSchemaIfEmpty(t, bunDB)
		store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: storeID})
		if err != nil {
			_ = bunDB.Close()
			t.Fatalf("open durable store %d: %v", i, err)
		}
		t.Cleanup(func() { _ = store.Close() })
		stores[i] = store
	}
	return stores
}

// phase10V2ConcurrentSubmissionInputs admits and closes two valid V2 calls that
// share one trusted submission through the production owner-aware APIs. Each
// result carries a 20-nano submission fee plus a 100-nano call fee (customer
// charge 120) under a 100-nano admitted exposure, so exactly one call must win
// the immutable fee claim and settle with a 20-nano overrun while the other
// settles at 100 as the fee-deducting sibling.
func phase10V2ConcurrentSubmissionInputs(t *testing.T, store *DurableStore, accountID, submissionID string) [2]billing.ApplyCallBillingInput {
	t.Helper()
	var inputs [2]billing.ApplyCallBillingInput
	for i := range inputs {
		call, exposure := phase10V2SubmissionCall(t, store, accountID, submissionID, fmt.Sprintf("b-concurrent-%d", i), 100)
		result := phase10V2SubmissionResult(t, call, 20, 100)
		inputs[i] = billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result, PostingOwner: billing.PostingOwnerV2}
	}
	return inputs
}

// phase10SettleConcurrently releases both settlements from one barrier so the
// first-claim attempts truly compete rather than being serialized by test
// scheduling.
func phase10SettleConcurrently(ctx context.Context, settlers [2]*DurableStore, inputs [2]billing.ApplyCallBillingInput) ([2]billing.CallSettlement, [2]error) {
	var settlements [2]billing.CallSettlement
	var errs [2]error
	var group sync.WaitGroup
	start := make(chan struct{})
	for i := range settlers {
		group.Add(1)
		go func(idx int) {
			defer group.Done()
			<-start
			settlements[idx], errs[idx] = settlers[idx].ApplyCallBillingResult(ctx, inputs[idx])
		}(i)
	}
	close(start)
	group.Wait()
	return settlements, errs
}

// phase10AssertConcurrentFirstClaimMoney asserts the durable money outcome of
// the concurrent first-claim race: a 780 balance, exactly two financial
// transactions debiting 120 (owner) and 100 (sibling), and the two expected
// transaction identities.
func phase10AssertConcurrentFirstClaimMoney(t *testing.T, ctx context.Context, store *DurableStore, accountID, ownerCallID, siblingCallID, ownerTxID, siblingTxID string) {
	t.Helper()
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.BalanceNano != 780 {
		t.Fatalf("balance = %d, want 780 (1000 - 120 owner - 100 sibling)", account.BalanceNano)
	}
	transactions, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(transactions) != 2 {
		t.Fatalf("journal transactions = %d, want exactly two", len(transactions))
	}
	debitByCall := map[string]int64{}
	txByCall := map[string]string{}
	for _, tx := range transactions {
		var debit int64
		for _, entry := range tx.Entries {
			if entry.Side == billing.JournalDebit && entry.LedgerAccount == "customer_financial_account" {
				debit += entry.Amount.Nano
			}
		}
		debitByCall[tx.TurnID] = debit
		txByCall[tx.TurnID] = tx.ID
	}
	if debitByCall[ownerCallID] != 120 {
		t.Fatalf("owner call debit = %d, want 120", debitByCall[ownerCallID])
	}
	if debitByCall[siblingCallID] != 100 {
		t.Fatalf("sibling call debit = %d, want 100", debitByCall[siblingCallID])
	}
	if total := debitByCall[ownerCallID] + debitByCall[siblingCallID]; total != 220 {
		t.Fatalf("total debit = %d, want 220", total)
	}
	if txByCall[ownerCallID] != ownerTxID || txByCall[siblingCallID] != siblingTxID {
		t.Fatalf("journal transactions = %+v, want owner %q and sibling %q", txByCall, ownerTxID, siblingTxID)
	}
}

// phase10RequireCompletedPin loads and validates one completed V2 customer
// settlement pin bound to the expected posting transaction.
func phase10RequireCompletedPin(t *testing.T, ctx context.Context, store *DurableStore, sourceKey, wantTxID string) billing.PostingPin {
	t.Helper()
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, sourceKey)
	if err != nil {
		t.Fatalf("load customer settlement pin %q: %v", sourceKey, err)
	}
	if pin.Owner != billing.PostingOwnerV2 || !pin.IsCompleted() || pin.CompletionTransactionID != wantTxID {
		t.Fatalf("customer settlement pin %q = %+v, want completed V2 owner tx %q", sourceKey, pin, wantTxID)
	}
	return pin
}

// phase10AssertV2ConcurrentFirstClaimAndReplay drives the R2 concurrent
// first-claim acceptance: two handles settle the shared-submission calls at one
// barrier, the durable immutable claim elects exactly one owner, both exposures
// close with completed original pins, and replaying both exact inputs on fresh
// handles preserves the dispositions with zero new claim, journal, pin, balance,
// or exposure effect.
func phase10AssertV2ConcurrentFirstClaimAndReplay(t *testing.T, ctx context.Context, accountID, submissionID string, settlers [2]*DurableStore, openReplayers func() [2]*DurableStore, inputs [2]billing.ApplyCallBillingInput) {
	t.Helper()
	settlements, settleErrs := phase10SettleConcurrently(ctx, settlers, inputs)
	for i, err := range settleErrs {
		if err != nil {
			t.Fatalf("concurrent first settlement %d (%s): %v", i, inputs[i].Call.CallID, err)
		}
	}

	// Elect the owner from the immutable claim, never from goroutine order.
	ownerCallID, claimAmount := phase10SubmissionClaimRow(t, settlers[0], accountID, submissionID)
	if claimAmount != 20 {
		t.Fatalf("submission fee claim amount = %d, want 20", claimAmount)
	}
	if count := phase10SubmissionClaimCount(t, settlers[0], accountID, submissionID); count != 1 {
		t.Fatalf("submission fee claim count = %d, want exactly one", count)
	}
	owner := -1
	for i := range inputs {
		if inputs[i].Call.CallID.String() == ownerCallID {
			owner = i
		}
	}
	if owner < 0 {
		t.Fatalf("submission fee claim owner %q is not one of the settled calls", ownerCallID)
	}
	sibling := 1 - owner

	if settlements[owner].Replayed || !settlements[owner].Breached || settlements[owner].OverrunNano != 20 {
		t.Fatalf("owner settlement = %+v, want fresh 120-nano posting with 20-nano overrun", settlements[owner])
	}
	if settlements[sibling].Replayed || settlements[sibling].Breached || settlements[sibling].OverrunNano != 0 {
		t.Fatalf("sibling settlement = %+v, want fresh non-breached 100-nano posting", settlements[sibling])
	}
	ownerTxID := settlements[owner].Customer.Transaction.ID
	siblingTxID := settlements[sibling].Customer.Transaction.ID
	if ownerTxID == "" || siblingTxID == "" || ownerTxID == siblingTxID {
		t.Fatalf("settlement journal transactions = (%q, %q), want two distinct transactions", ownerTxID, siblingTxID)
	}
	ownerCall := inputs[owner].Call.CallID
	siblingCall := inputs[sibling].Call.CallID
	phase10AssertConcurrentFirstClaimMoney(t, ctx, settlers[0], accountID, ownerCall.String(), siblingCall.String(), ownerTxID, siblingTxID)

	ownerSourceKey, err := billing.CustomerSettlementSourceKey(accountID, ownerCall)
	if err != nil {
		t.Fatal(err)
	}
	siblingSourceKey, err := billing.CustomerSettlementSourceKey(accountID, siblingCall)
	if err != nil {
		t.Fatal(err)
	}
	ownerPin := phase10RequireCompletedPin(t, ctx, settlers[0], ownerSourceKey, ownerTxID)
	siblingPin := phase10RequireCompletedPin(t, ctx, settlers[0], siblingSourceKey, siblingTxID)
	phase10AssertExposureState(t, settlers[0], ownerCall, inputs[owner].Exposure.Fingerprint)
	phase10AssertExposureState(t, settlers[0], siblingCall, inputs[sibling].Exposure.Fingerprint)

	// Replay both exact inputs on fresh handles: dispositions and durable state
	// must be identical to the elected outcome.
	replayers := openReplayers()
	ownerReplay, err := replayers[owner].ApplyCallBillingResult(ctx, inputs[owner])
	if err != nil {
		t.Fatalf("owner exact replay after owner election: %v", err)
	}
	if !ownerReplay.Replayed || !ownerReplay.Breached || ownerReplay.OverrunNano != 20 {
		t.Fatalf("owner replay = %+v, want replayed 20-nano overrun", ownerReplay)
	}
	siblingReplay, err := replayers[sibling].ApplyCallBillingResult(ctx, inputs[sibling])
	if err != nil {
		t.Fatalf("sibling exact replay after owner election: %v", err)
	}
	if !siblingReplay.Replayed || siblingReplay.Breached || siblingReplay.OverrunNano != 0 {
		t.Fatalf("sibling replay = %+v, want replayed non-breached settlement", siblingReplay)
	}

	if replayOwnerCallID, replayAmount := phase10SubmissionClaimRow(t, replayers[0], accountID, submissionID); replayOwnerCallID != ownerCallID || replayAmount != 20 {
		t.Fatalf("claim after replay = (%q, %d), want unchanged (%q, 20)", replayOwnerCallID, replayAmount, ownerCallID)
	}
	if count := phase10SubmissionClaimCount(t, replayers[0], accountID, submissionID); count != 1 {
		t.Fatalf("claim count after replay = %d, want one", count)
	}
	phase10AssertConcurrentFirstClaimMoney(t, ctx, replayers[0], accountID, ownerCall.String(), siblingCall.String(), ownerTxID, siblingTxID)
	if afterOwnerPin := phase10RequireCompletedPin(t, ctx, replayers[0], ownerSourceKey, ownerTxID); afterOwnerPin != ownerPin {
		t.Fatalf("owner replay changed pin: before=%+v after=%+v", ownerPin, afterOwnerPin)
	}
	if afterSiblingPin := phase10RequireCompletedPin(t, ctx, replayers[0], siblingSourceKey, siblingTxID); afterSiblingPin != siblingPin {
		t.Fatalf("sibling replay changed pin: before=%+v after=%+v", siblingPin, afterSiblingPin)
	}
	phase10AssertExposureState(t, replayers[0], ownerCall, inputs[owner].Exposure.Fingerprint)
	phase10AssertExposureState(t, replayers[0], siblingCall, inputs[sibling].Exposure.Fingerprint)
}
