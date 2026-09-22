package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestDurableStoreCallSettlementAppliesCustomerUnitOperationAtomically(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "unit-settlement", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID, ALegID: "a-unit-settlement", SessionID: "s", StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(), Outcome: billing.TurnOutcomeCompleted, CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"}, ExpectedBLegIDs: []string{"b"}}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 60, Currency: "USD"}, PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef})
	if err != nil {
		t.Fatal(err)
	}
	key := customerUnitTestKey(account.ID, "included", "2026-09")
	grant := customerUnitOperation("unit-grant-settlement", key, billing.CustomerUnitOperationGrant, "", "4", 0, 1)
	grantResult, err := store.ApplyCustomerUnitOperation(ctx, grant)
	if err != nil {
		t.Fatal(err)
	}
	debit := customerUnitOperation("unit-debit-settlement", key, billing.CustomerUnitOperationDebit, "", "3", grantResult.After.Version, 1)
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: billing.CallRatingResult{
		CallID: callID, CustomerCharge: billing.Money{Nano: 25, Currency: "USD"}, Fingerprint: "unit-settlement-result", CustomerUnitOperation: &debit,
	}})
	if err != nil {
		t.Fatalf("settlement: %v", err)
	}
	if settled.CustomerUnitResult == nil || settled.CustomerUnitResult.After.Consumed.CanonicalString() != "3/0" {
		t.Fatalf("settled customer-unit result = %#v", settled.CustomerUnitResult)
	}
	balance, err := store.CustomerUnitBalance(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if balance.Available.CanonicalString() != "1/0" || balance.Consumed.CanonicalString() != "3/0" {
		t.Fatalf("customer-unit balance = %#v", balance)
	}
}

func TestDurableStoreCallSettlementRollsBackCustomerUnitOperationOnFallbackBoundFailure(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "unit-settlement-rollback", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID, ALegID: "a-unit-rollback", StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(), Outcome: billing.TurnOutcomeCompleted, CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"}}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 60, Currency: "USD"}, PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef})
	if err != nil {
		t.Fatal(err)
	}
	key := customerUnitTestKey(account.ID, "included", "2026-09")
	grant := customerUnitOperation("unit-grant-rollback", key, billing.CustomerUnitOperationGrant, "", "2", 0, 1)
	grantResult, err := store.ApplyCustomerUnitOperation(ctx, grant)
	if err != nil {
		t.Fatal(err)
	}
	debit := customerUnitOperation("unit-debit-rollback", key, billing.CustomerUnitOperationDebit, "", "3", grantResult.After.Version, 1)
	debit.MonetaryFallbackBound = &billing.Money{Nano: 10, Currency: "USD"}
	_, err = store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: billing.CallRatingResult{
		CallID: callID, CustomerCharge: billing.Money{Nano: 25, Currency: "USD"}, Fingerprint: "unit-rollback-result", CustomerUnitOperation: &debit, CustomerUnitFallbackCharge: &billing.Money{Nano: 11, Currency: "USD"},
	}})
	if err == nil {
		t.Fatal("settlement with fallback amount above bound unexpectedly succeeded")
	}
	if !errors.Is(err, billing.ErrCustomerUnitInvalid) {
		t.Fatalf("fallback bound error = %v, want ErrCustomerUnitInvalid", err)
	}
	balance, err := store.CustomerUnitBalance(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if balance.Available.CanonicalString() != "2/0" || balance.Consumed.CanonicalString() != "0/0" {
		t.Fatalf("rolled-back customer-unit balance = %#v", balance)
	}
	var operations int
	if err := store.db.NewRaw(`SELECT COUNT(*) FROM billing_unit_operations WHERE operation_id = ?`, debit.OperationID).Scan(ctx, &operations); err != nil {
		t.Fatal(err)
	}
	if operations != 0 {
		t.Fatalf("rolled-back customer-unit operation rows = %d, want 0", operations)
	}
}
