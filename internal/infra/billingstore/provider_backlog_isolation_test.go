package billingstore

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Task 14.3 certification: supplier costing/reconciliation backlog never takes
// the customer admission account-balance lock. While a provider-cost backlog
// drains, customer admission and settlement proceed with exact balances, and
// supplier-only work leaves the customer account untouched.

func TestSQLiteSupplierBacklogDoesNotBlockCustomerAdmission(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := newSQLiteTestStore(t)
	account := billing.Account{ID: "supplier-backlog", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	supplierCallID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	supplierCall := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: supplierCallID, AccountID: account.ID,
		ALegID: "a-supplier", SessionID: "sess-supplier", StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v2"},
		ExpectedBLegIDs: []string{"b-1", "b-2", "b-3", "b-4"},
	}
	if err := store.AppendCallUsage(ctx, supplierCall); err != nil {
		t.Fatal(err)
	}
	legs := make([]billing.CallLegUsageRecord, 0, 4)
	for i, bLegID := range []string{"b-1", "b-2", "b-3", "b-4"} {
		leg := testIndependentCallLegFor(supplierCallID, bLegID)
		leg.ALegID = "a-supplier"
		leg.AttemptSeq = i + 1
		if err := store.AppendCallLegUsage(ctx, leg); err != nil {
			t.Fatal(err)
		}
		sealed, err := leg.Seal()
		if err != nil {
			t.Fatal(err)
		}
		legs = append(legs, sealed)
	}
	pending, err := store.ListPendingProviderCostWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 4 {
		t.Fatalf("pending supplier backlog = %d, want 4", len(pending))
	}
	pre, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pre.BalanceNano != 1000 || pre.Version != 1 {
		t.Fatalf("backlog creation mutated customer account: %+v", pre)
	}

	customerCallID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	customerCall := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: customerCallID, AccountID: account.ID,
		ALegID: "a-customer", SessionID: "sess-customer", StartedAt: time.Unix(200, 0).UTC(), FinishedAt: time.Unix(201, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v2"},
	}
	if err := store.AppendCallUsage(ctx, customerCall); err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		for i, leg := range legs {
			cost := billing.OperatorCostResult{LURKey: leg.Key, Amount: billing.Money{Nano: int64(5 + i), Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
			if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: supplierCallID, Leg: leg, Result: cost}); err != nil {
				errs <- err
				return
			}
		}
		errs <- nil
	}()
	go func() {
		defer group.Done()
		exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
			AccountID: account.ID, CallID: customerCallID.String(), Max: billing.Money{Nano: 100, Currency: "USD"},
			PricingRef: customerCall.CustomerPricingRef, ChargePolicyRef: customerCall.ChargePolicyRef,
		})
		if err != nil {
			errs <- err
			return
		}
		if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
			Call: customerCall, Exposure: exposure,
			Result: billing.CallRatingResult{CallID: customerCallID, CustomerCharge: billing.Money{Nano: 30, Currency: "USD"}, Fingerprint: "backlog-customer"},
		}); err != nil {
			errs <- err
			return
		}
		errs <- nil
	}()
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent supplier/customer work: %v", err)
		}
	}

	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 970 {
		t.Fatalf("balance = %d, want 1000-30 customer actual (supplier work is not a customer debit)", got.BalanceNano)
	}
	if got.Version != pre.Version+1 {
		t.Fatalf("version = %d, want %d (exactly one customer settlement bump; supplier work bumps nothing)", got.Version, pre.Version+1)
	}
	remaining, err := store.ListPendingProviderCostWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("pending supplier backlog = %d, want fully drained", len(remaining))
	}
	closed, err := store.GetCallExposure(ctx, customerCallID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.IsOpen() {
		t.Fatal("customer exposure must close while supplier backlog drains")
	}
}
