package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteOperatorCostReportPagesIndependentLegsWithDateFilter(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "operator-indep-page", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	earlyID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	lateID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	earlyFinished := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	lateFinished := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	appendIndependentLegAt(t, store, account.ID, earlyID, "b-early", earlyFinished, 5)
	appendIndependentLegAt(t, store, account.ID, lateID, "b-late", lateFinished, 9)

	first, err := store.OperatorCostReport(ctx, billing.ReportFilter{
		AccountID: account.ID,
		From:      time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
		Page:      billing.PageRequest{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Rows) != 1 || first.Rows[0].BLegID != "b-late" || first.Rows[0].Amount.Nano != 9 {
		t.Fatalf("date-filtered rows = %+v, want only b-late=9", first.Rows)
	}

	page1, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: account.ID, Page: billing.PageRequest{Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Rows) != 1 || page1.NextKey == "" {
		t.Fatalf("page1 = rows=%d next=%q", len(page1.Rows), page1.NextKey)
	}
	page2, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: account.ID, Page: billing.PageRequest{Limit: 1, AfterKey: page1.NextKey}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Rows) != 1 || page2.Rows[0].BLegID == page1.Rows[0].BLegID {
		t.Fatalf("page2 did not advance: page1=%+v page2=%+v", page1.Rows, page2.Rows)
	}
}

func appendIndependentLegAt(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, bLegID string, finishedAt time.Time, providerNano int64) {
	t.Helper()
	ctx := context.Background()
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID,
		ALegID: "a-1", SessionID: "sess-1",
		StartedAt: finishedAt.Add(-time.Second), FinishedAt: finishedAt,
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v2"},
		ExpectedBLegIDs: []string{bLegID},
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, bLegID)
	leg.StartedAt = finishedAt.Add(-time.Second)
	leg.FinishedAt = finishedAt
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	cost := billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: providerNano, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: accountID, CallID: callID, Leg: leg, Result: cost}); err != nil {
		t.Fatal(err)
	}
}

func settleIndependentCall(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, aLegID, sessionID string, customerNano, providerNano int64) {
	t.Helper()
	ctx := context.Background()
	pricing := billing.VersionRef{ID: "prices", Version: "v1"}
	policy := billing.VersionRef{ID: "policy", Version: "v2"}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID,
		ALegID: aLegID, SessionID: sessionID,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.TurnOutcomeCompleted, CustomerPricingRef: pricing, ChargePolicyRef: policy,
		ExpectedBLegIDs: []string{"b-1"},
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-1")
	leg.ALegID = aLegID
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(), Max: billing.Money{Nano: 100, Currency: "USD"},
		PricingRef: pricing, ChargePolicyRef: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: customerNano, Currency: "USD"}, Fingerprint: "fp-" + callID.String()}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result}); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	cost := billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: providerNano, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: accountID, CallID: callID, Leg: leg, Result: cost}); err != nil {
		t.Fatal(err)
	}
}
