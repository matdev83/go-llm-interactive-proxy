package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteSettlementPostsIndependentB2BUALURCosts(t *testing.T) {
	runSettlementB2BUALURCosts(t, newSQLiteTestStore(t), "b2bua-settlement")
}

func runSettlementB2BUALURCosts(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authorize(ctx, authorizationInput(accountID, "turn", "auth", 40))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(300, 0).UTC()
	record := billing.TurnUsageRecord{SchemaVersion: billing.CurrentRecordSchemaVersion, AccountID: accountID, TurnID: "turn", ALegID: "a-1", AuthorizationID: "auth", StartedAt: now, FinishedAt: now.Add(2 * time.Second), Outcome: billing.TurnOutcomeCompleted, CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"}, Legs: []billing.LegUsageRecord{
		{ALegID: "a-1", BLegID: "failed-provider-a", Seq: 1, BackendID: "backend-a", ProviderID: "provider-a", ModelID: "model-a", StartedAt: now, FinishedAt: now.Add(time.Second), Outcome: billing.LegOutcomeFailed, Surfaced: billing.SurfacedNo, Evidence: billing.FinalBillingEvidence{Cost: billing.MoneyEvidence{NanoUnits: 2, Currency: "USD", Present: true}}},
		{ALegID: "a-1", BLegID: "winner-provider-b", Seq: 2, BackendID: "backend-b", ProviderID: "provider-b", ModelID: "model-b", StartedAt: now, FinishedAt: now.Add(2 * time.Second), Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes, Evidence: billing.FinalBillingEvidence{Cost: billing.MoneyEvidence{NanoUnits: 5, Currency: "USD", Present: true}}},
	}}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsageRecord(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	result := billing.Result{TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 8, Currency: "USD"}, OperatorCosts: []billing.OperatorCostResult{
		{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 2, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true},
		{LURKey: sealed.Legs[1].Key, Amount: billing.Money{Nano: 5, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true},
	}}
	settlement, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	if len(settlement.ProviderCosts) != 2 || settlement.ProviderCosts[0].Transaction.BLegID != "failed-provider-a" || settlement.ProviderCosts[1].Transaction.BLegID != "winner-provider-b" {
		t.Fatalf("provider settlement = %+v", settlement.ProviderCosts)
	}
	journals, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, journal := range journals {
		if journal.OperationKind == "provider_cogs" {
			seen[journal.BLegID] = true
		}
	}
	if !seen["failed-provider-a"] || !seen["winner-provider-b"] {
		t.Fatalf("provider B-leg identities = %#v", seen)
	}
}
