package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteCustomerSettlementIndependentOfProviderCostOrdering(t *testing.T) {
	t.Parallel()
	t.Run("settle then provider cost", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		ctx := context.Background()
		account, call, exposure, leg, sealed := seedIndependentSettlementFixture(t, store, "indep-settle-first", 100)
		result := billing.CallRatingResult{CallID: call.CallID, CustomerCharge: billing.Money{Nano: 25, Currency: "USD"}, Fingerprint: "settle-first"}
		if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result}); err != nil {
			t.Fatal(err)
		}
		pending, err := store.ListPendingProviderCostWork(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) != 1 {
			t.Fatalf("pending provider work after customer settle = %d, want 1", len(pending))
		}
		got, err := store.GetAccount(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.BalanceNano != 75 {
			t.Fatalf("balance after settle = %d, want 75", got.BalanceNano)
		}
		var status string
		if err := store.db.NewRaw(`SELECT status FROM call_exposures WHERE call_id = ?`, call.CallID.String()).Scan(ctx, &status); err != nil {
			t.Fatal(err)
		}
		if status != "closed" {
			t.Fatalf("exposure status = %q, want closed while provider pending", status)
		}
		cost := billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 11, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
		if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: call.CallID, Leg: leg, Result: cost}); err != nil {
			t.Fatal(err)
		}
		after, err := store.GetAccount(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.BalanceNano != 75 || after.Version != got.Version {
			t.Fatalf("provider cost mutated settled customer account: before=%+v after=%+v", got, after)
		}
		if err := store.db.NewRaw(`SELECT status FROM call_exposures WHERE call_id = ?`, call.CallID.String()).Scan(ctx, &status); err != nil {
			t.Fatal(err)
		}
		if status != "closed" {
			t.Fatalf("exposure reopened after provider cost: %q", status)
		}
	})

	t.Run("provider cost then settle", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		ctx := context.Background()
		account, call, exposure, leg, sealed := seedIndependentSettlementFixture(t, store, "indep-cost-first", 100)
		cost := billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 11, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
		if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: call.CallID, Leg: leg, Result: cost}); err != nil {
			t.Fatal(err)
		}
		before, err := store.GetAccount(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if before.BalanceNano != 100 {
			t.Fatalf("balance after provider-only = %d, want 100", before.BalanceNano)
		}
		result := billing.CallRatingResult{CallID: call.CallID, CustomerCharge: billing.Money{Nano: 25, Currency: "USD"}, Fingerprint: "cost-first"}
		if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result}); err != nil {
			t.Fatal(err)
		}
		after, err := store.GetAccount(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.BalanceNano != 75 {
			t.Fatalf("balance after settle = %d, want 75", after.BalanceNano)
		}
	})

	t.Run("two B-legs interleaved with customer settle", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		ctx := context.Background()
		account := billing.Account{ID: "indep-two-legs", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 200, State: billing.AccountReady, Version: 1}
		if err := store.CreateAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		callID, err := billing.NewBillingCallID()
		if err != nil {
			t.Fatal(err)
		}
		pricing := billing.VersionRef{ID: "prices", Version: "v1"}
		policy := billing.VersionRef{ID: "policy", Version: "v2"}
		call := billing.CallUsageRecord{
			SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID,
			ALegID: "a-1", SessionID: "sess-1", StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(102, 0).UTC(),
			Outcome: billing.TurnOutcomeCompleted, CustomerPricingRef: pricing, ChargePolicyRef: policy,
			ExpectedBLegIDs: []string{"b-1", "b-2"},
		}
		if err := store.AppendCallUsage(ctx, call); err != nil {
			t.Fatal(err)
		}
		leg1 := testIndependentCallLegFor(callID, "b-1")
		leg1.AttemptSeq = 1
		leg2 := testIndependentCallLegFor(callID, "b-2")
		leg2.AttemptSeq = 2
		if err := store.AppendCallLegUsage(ctx, leg1); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendCallLegUsage(ctx, leg2); err != nil {
			t.Fatal(err)
		}
		exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
			AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 100, Currency: "USD"},
			PricingRef: pricing, ChargePolicyRef: policy,
		})
		if err != nil {
			t.Fatal(err)
		}
		sealed1, err := leg1.Seal()
		if err != nil {
			t.Fatal(err)
		}
		cost1 := billing.OperatorCostResult{LURKey: sealed1.Key, Amount: billing.Money{Nano: 5, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
		if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: callID, Leg: leg1, Result: cost1}); err != nil {
			t.Fatal(err)
		}
		result := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 40, Currency: "USD"}, Fingerprint: "two-legs"}
		if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result}); err != nil {
			t.Fatal(err)
		}
		sealed2, err := leg2.Seal()
		if err != nil {
			t.Fatal(err)
		}
		cost2 := billing.OperatorCostResult{LURKey: sealed2.Key, Amount: billing.Money{Nano: 8, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
		if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: callID, Leg: leg2, Result: cost2}); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetAccount(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.BalanceNano != 160 {
			t.Fatalf("balance = %d, want 160", got.BalanceNano)
		}
		pending, err := store.ListPendingProviderCostWork(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) != 0 {
			t.Fatalf("pending provider work = %d, want 0", len(pending))
		}
	})
}

func seedIndependentSettlementFixture(t *testing.T, store *DurableStore, accountID string, balance int64) (billing.Account, billing.CallUsageRecord, billing.CallExposure, billing.CallLegUsageRecord, billing.CallLegUsageRecord) {
	t.Helper()
	ctx := context.Background()
	account := billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: balance, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	pricing := billing.VersionRef{ID: "prices", Version: "v1"}
	policy := billing.VersionRef{ID: "policy", Version: "v2"}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID,
		ALegID: "a-1", SessionID: "sess-1", StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.TurnOutcomeCompleted, CustomerPricingRef: pricing, ChargePolicyRef: policy,
		ExpectedBLegIDs: []string{"b-1"},
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-1")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 80, Currency: "USD"},
		PricingRef: pricing, ChargePolicyRef: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	return account, call, exposure, leg, sealed
}
