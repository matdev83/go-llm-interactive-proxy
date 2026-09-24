package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Task 14.2 RED: overrun settlement posts the actual incurred amount in the
// same atomic transaction instead of discarding it. Breach state is explicit,
// replays stay idempotent, and conflicting actuals conflict.

//nolint:revive // test helper keeps t first per Go testing convention
func overrunCallFixture(t *testing.T, store *DurableStore, ctx context.Context, account billing.Account, maxNano, actualNano int64) (billing.CallUsageRecord, billing.CallExposure, billing.CallRatingResult) {
	t.Helper()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID, ALegID: "a-overrun",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(), Outcome: billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v2"},
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: maxNano, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: actualNano, Currency: "USD"}, Fingerprint: "overrun-result"}
	return call, exposure, result
}

func TestSQLiteApplyCallBillingResultOverrunPostsActualWithBreach(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "call-breach", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	call, exposure, result := overrunCallFixture(t, store, ctx, account, 10, 25)
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result})
	if err != nil {
		t.Fatalf("overrun settle = %v, want success posting actual 25", err)
	}
	if settled.Replayed {
		t.Fatal("first overrun settlement was marked replayed")
	}
	if !settled.Breached || settled.OverrunNano != 15 {
		t.Fatalf("settlement = %+v, want Breached with OverrunNano 15", settled)
	}
	gotAccount, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotAccount.BalanceNano != 75 || gotAccount.State != billing.AccountReady {
		t.Fatalf("account after breach settlement = %+v, want balance 75 ready", gotAccount)
	}
	var status string
	if err := store.db.NewRaw(`SELECT status FROM call_exposures WHERE call_id = ?`, call.CallID.String()).Scan(ctx, &status); err != nil {
		t.Fatal(err)
	}
	if status != "closed" {
		t.Fatalf("exposure status = %q, want closed", status)
	}
	transactions, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	var posted int64 = -1
	for _, transaction := range transactions {
		if transaction.OperationKind != "customer_call_settlement" {
			continue
		}
		for _, entry := range transaction.Entries {
			if entry.Side == billing.JournalDebit {
				posted = entry.Amount.Nano
			}
		}
	}
	if posted != 25 {
		t.Fatalf("posted debit = %d, want actual 25 (not the 10 quote)", posted)
	}
	var claimStatus string
	if err := store.db.NewRaw(`SELECT claim_status FROM usage_call_records WHERE call_id = ?`, call.CallID.String()).Scan(ctx, &claimStatus); err != nil {
		t.Fatal(err)
	}
	if claimStatus != "processed" {
		t.Fatalf("claim status = %q, want processed", claimStatus)
	}
	// Identical redelivery must replay without a second financial effect.
	replayed, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result})
	if err != nil {
		t.Fatalf("breach replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatal("identical breach redelivery was not marked replayed")
	}
	afterReplay, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterReplay.BalanceNano != 75 || afterReplay.Version != gotAccount.Version {
		t.Fatalf("breach replay mutated account: %+v vs %+v", gotAccount, afterReplay)
	}
	// A different actual under the same call identity must conflict rather
	// than silently rewrite the posted cost to another value.
	conflict := result
	conflict.CustomerCharge.Nano = 26
	conflict.Fingerprint = "overrun-different-actual"
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: conflict}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("conflicting actual = %v, want ErrOperationConflict", err)
	}
}

func TestSQLiteApplyCallBillingResultFailedCallSettlesActualWithoutBypass(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "call-failed", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID, ALegID: "a-failed",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(), Outcome: billing.TurnOutcomeFailed,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v2"},
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 60, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exposure,
		Result: billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 18, Currency: "USD"}, Fingerprint: "failed-actual"},
	})
	if err != nil {
		t.Fatalf("failed-call settle = %v", err)
	}
	if settled.Breached {
		t.Fatalf("settlement = %+v, within-max failed call must not breach", settled)
	}
	gotAccount, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotAccount.BalanceNano != 82 {
		t.Fatalf("account after failed-call settlement = %+v, want actual 18 debited", gotAccount)
	}
	closed, err := store.GetCallExposure(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.IsOpen() {
		t.Fatal("failed-call exposure must close through settlement, not bypass it")
	}
}
