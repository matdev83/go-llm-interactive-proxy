package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteQueryOpenExposuresPagesOpenRowsWithCallCorrelation(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "open-exposure-page", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	firstID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	pricing := billing.VersionRef{ID: "prices", Version: "v1"}
	policy := billing.VersionRef{ID: "policy", Version: "v2"}
	for _, callID := range []billing.BillingCallID{firstID, secondID} {
		if _, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
			AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 25, Currency: "USD"},
			PricingRef: pricing, ChargePolicyRef: policy, Now: time.Unix(50, 0).UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: firstID, AccountID: account.ID,
		ALegID: "a-open", SessionID: "sess-open",
		StartedAt: time.Unix(50, 0).UTC(), FinishedAt: time.Unix(51, 0).UTC(),
		Outcome: billing.TurnOutcomeFailed, CustomerPricingRef: pricing, ChargePolicyRef: policy,
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}

	page1, err := store.QueryOpenExposures(ctx, account.ID, billing.PageRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Items) != 1 || page1.NextCursor == "" {
		t.Fatalf("page1 = %+v, want one item and cursor", page1)
	}
	page2, err := store.QueryOpenExposures(ctx, account.ID, billing.PageRequest{Limit: 10, AfterKey: page1.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 1 {
		t.Fatalf("page2 = %+v, want remaining open exposure", page2)
	}
	byCall := map[string]billing.ExposureReport{}
	for _, item := range append(append([]billing.ExposureReport{}, page1.Items...), page2.Items...) {
		byCall[item.CallID] = item
		if item.Status != billing.ExposureOpen || item.Max.Nano != 25 {
			t.Fatalf("open exposure = %+v", item)
		}
	}
	if byCall[firstID.String()].ALegID != "a-open" || byCall[firstID.String()].SessionID != "sess-open" {
		t.Fatalf("first exposure correlation = %+v", byCall[firstID.String()])
	}
}

func TestSQLiteCallExplanationCorrelatesExposureUsageSettlementAndProviderCost(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "call-explain", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	settleIndependentCall(t, store, account.ID, callID, "a-explain", "sess-explain", 10, 3)

	got, err := store.CallExplanation(ctx, callID.String())
	if err != nil {
		t.Fatal(err)
	}
	if got.CallID != callID.String() {
		t.Fatalf("call id = %q", got.CallID)
	}
	if got.Exposure.Status != billing.ExposureClosed || got.Exposure.ALegID != "a-explain" {
		t.Fatalf("exposure = %+v", got.Exposure)
	}
	if got.Closure.CallID != callID || len(got.Legs) != 1 {
		t.Fatalf("closure/legs = closure=%+v legs=%d", got.Closure, len(got.Legs))
	}
	if len(got.CustomerOperations) == 0 || len(got.ProviderCostOperations) == 0 {
		t.Fatalf("operations customer=%d provider=%d", len(got.CustomerOperations), len(got.ProviderCostOperations))
	}
	if !got.Result.Processed || got.Result.CustomerCharge.Nano != 10 || got.Result.ProviderCost.Nano != 3 {
		t.Fatalf("result = %+v", got.Result)
	}
}

func TestSQLiteCallExplanationMissingCallIsNotFound(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CallExplanation(context.Background(), callID.String())
	if !errors.Is(err, billing.ErrReportNotFound) {
		t.Fatalf("missing call explanation = %v, want ErrReportNotFound", err)
	}
}
