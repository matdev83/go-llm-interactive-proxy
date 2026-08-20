package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteApplyCallBillingResultExactZeroClosesExposureWithoutJournalEntry(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "call-zero", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion,
		CallID:        callID, AccountID: account.ID, ALegID: "a-zero",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeFailed,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v1"},
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
	result := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Currency: "USD"}, Fingerprint: "zero-call-result"}
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Replayed {
		t.Fatal("first exact-zero settlement was marked replayed")
	}
	after, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.BalanceNano != before.BalanceNano {
		t.Fatalf("exact-zero settlement mutated account: before=%+v after=%+v", before, after)
	}
	closed, err := store.GetCallExposure(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.IsOpen() || closed.Max.Nano != exposure.Max.Nano {
		t.Fatalf("exact-zero exposure = %+v", closed)
	}
	transactions, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, transaction := range transactions {
		if transaction.OperationKind == "customer_call_settlement" {
			t.Fatalf("exact-zero settlement wrote a financial journal transaction: %+v", transaction)
		}
	}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result}); err != nil {
		t.Fatalf("exact-zero replay: %v", err)
	}
}
