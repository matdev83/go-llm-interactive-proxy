package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteApplyCallBillingResultClosesExposureAndPostsCustomerCharge(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "call-settle", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID, ALegID: "a-call", SessionID: "session", StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(), Outcome: billing.TurnOutcomeCompleted, CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v2"}, ExpectedBLegIDs: []string{"b-1"}}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 60, Currency: "USD"}, PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef})
	if err != nil {
		t.Fatal(err)
	}
	result := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 25, Currency: "USD"}, Fingerprint: "call-result-fingerprint"}
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Replayed || settled.Customer.Transaction.ID == "" {
		t.Fatalf("settlement = %+v", settled)
	}
	expectedSource, err := billing.CustomerSettlementSourceKey(account.ID, callID)
	if err != nil {
		t.Fatal(err)
	}
	transactions, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	var foundSource bool
	for _, transaction := range transactions {
		if transaction.OperationKind == "customer_call_settlement" {
			foundSource = true
			if transaction.SourceKey != expectedSource {
				t.Fatalf("customer settlement source key = %q, want public helper %q", transaction.SourceKey, expectedSource)
			}
		}
	}
	if !foundSource {
		t.Fatal("customer settlement journal transaction not found")
	}
	gotAccount, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotAccount.BalanceNano != 75 || gotAccount.ReservedNano != 0 {
		t.Fatalf("account after settlement = %+v", gotAccount)
	}
	var status string
	if err := store.db.NewRaw(`SELECT status FROM call_exposures WHERE call_id = ?`, callID.String()).Scan(ctx, &status); err != nil {
		t.Fatal(err)
	}
	if status != "closed" {
		t.Fatalf("exposure status = %q, want closed", status)
	}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result}); err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	beforeConflict, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	conflict := result
	conflict.CustomerCharge.Nano = 26
	conflict.Fingerprint = "different-call-result-fingerprint"
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: conflict}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("closed-exposure conflicting replay = %v, want ErrOperationConflict", err)
	}
	afterConflict, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterConflict.BalanceNano != beforeConflict.BalanceNano || afterConflict.Version != beforeConflict.Version {
		t.Fatalf("closed-exposure conflict mutated account: before=%+v after=%+v", beforeConflict, afterConflict)
	}
}

func TestSQLiteApplyCallBillingResultActualExceedsMaxMarksReconcileRequired(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "call-over-max", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID, ALegID: "a-over",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(), Outcome: billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v2"},
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 10, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exposure,
		Result: billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 25, Currency: "USD"}, Fingerprint: "over-max"},
	})
	if !errors.Is(err, billing.ErrSettlementReconcileRequired) || !errors.Is(err, billing.ErrExposureActualExceedsMax) {
		t.Fatalf("settle over max = %v, want reconcile + exceeds max", err)
	}
	after, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != billing.AccountReconcileRequired {
		t.Fatalf("account state = %s, want reconcile_required", after.State)
	}
	if after.BalanceNano != before.BalanceNano {
		t.Fatalf("balance mutated: before=%d after=%d", before.BalanceNano, after.BalanceNano)
	}
	open, err := store.GetCallExposure(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if !open.IsOpen() {
		t.Fatal("exposure must remain open after reconcile-required overage")
	}
	transactions, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, transaction := range transactions {
		if transaction.OperationKind == "customer_call_settlement" {
			t.Fatalf("over-max settlement posted journal: %+v", transaction)
		}
	}
	var claimStatus string
	if err := store.db.NewRaw(`SELECT claim_status FROM usage_call_records WHERE call_id = ?`, callID.String()).Scan(ctx, &claimStatus); err != nil {
		t.Fatal(err)
	}
	if claimStatus == "processed" {
		t.Fatal("over-max settlement must not mark call processed")
	}
}

func TestSQLiteApplyCallBillingResultFloorViolationMarksReconcileRequired(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "call-floor", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID, ALegID: "a-floor",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(), Outcome: billing.TurnOutcomeCompleted,
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
	// Concurrent drain after admission: charge stays within admitted max but
	// crosses the prepaid floor at settlement time.
	if _, err := store.db.NewRaw(`UPDATE billing_accounts SET balance_nano = 10, version = version + 1 WHERE account_id = ?`, account.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exposure,
		Result: billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 50, Currency: "USD"}, Fingerprint: "floor-hit"},
	})
	if !errors.Is(err, billing.ErrSettlementReconcileRequired) || !errors.Is(err, billing.ErrInsufficientSpendable) {
		t.Fatalf("settle floor = %v, want reconcile + insufficient spendable", err)
	}
	after, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != billing.AccountReconcileRequired || after.BalanceNano != 10 {
		t.Fatalf("account after floor violation = %+v", after)
	}
	open, err := store.GetCallExposure(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if !open.IsOpen() {
		t.Fatal("exposure must remain open after floor reconcile-required")
	}
}
