package runtimebundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
)

// Phase 17.3 R4 runtime composition: a decorator that forwards valid claimed
// work but drops the cutover token (or clears OperationKey) must fail closed
// through actual production worker construction, with zero monetary effects.
// The decorator implements the mandatory WithCutover ports so composition
// accepts it; workers must reject the missing token.

type r4DropCustomerClaimer struct {
	inner *billingstore.DurableStore
}

func (d r4DropCustomerClaimer) ClaimCompleteCallsWithCutover(ctx context.Context, limit int) ([]billing.ClaimedCompleteCall, error) {
	items, err := d.inner.ClaimCompleteCallsWithCutover(ctx, limit)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Claim = billing.CutoverClaimMetadata{}
	}
	return items, nil
}

type r4DropProviderClaimer struct {
	inner *billingstore.DurableStore
}

func (d r4DropProviderClaimer) ClaimProviderCostWorkWithCutover(ctx context.Context, limit int) ([]billing.ClaimedProviderCostWork, error) {
	items, err := d.inner.ClaimProviderCostWorkWithCutover(ctx, limit)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Claim = billing.CutoverClaimMetadata{}
	}
	return items, nil
}

type r4DropEconomicClaimer struct {
	inner *billingstore.DurableStore
}

func (d r4DropEconomicClaimer) ClaimEconomicRevisionWorkWithCutover(ctx context.Context, work billing.EconomicRevisionWork, owner string, lease time.Duration) (billing.EconomicRevisionWorkClaim, *billing.CutoverClaimMetadata, bool, error) {
	claim, _, claimed, err := d.inner.ClaimEconomicRevisionWorkWithCutover(ctx, work, owner, lease)
	if err != nil || !claimed {
		return claim, nil, claimed, err
	}
	return claim, nil, true, nil
}

type r4RatingStub struct {
	charge int64
	fp     string
	call   billing.BillingCallID
}

func (s r4RatingStub) ResolveCallRating(_ context.Context, complete billing.CompleteCall, _ billing.CallExposure) (billing.CallRatingResult, error) {
	callID := complete.Closure.CallID
	if callID.String() == "" {
		callID = s.call
	}
	return billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: s.charge, Currency: "USD"}, Fingerprint: s.fp + "-" + callID.String()}, nil
}

type r4ProviderStub struct{}

func (r4ProviderStub) ResolveProviderCost(_ context.Context, leg billing.CallLegUsageRecord) (billing.OperatorCostResult, error) {
	sealed, err := leg.Seal()
	if err != nil {
		return billing.OperatorCostResult{}, err
	}
	return billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 11, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}, nil
}

func TestR4RuntimeDropCustomerTokenFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := f3rbNewStore(t, "r4-rt-cust")
	_ = f3rbCompose(t, store)
	accountID := "acct-r4-rt-cust"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 90000, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "r4-rt-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	prod := f3rbCompose(t, store)
	exp, err := prod.BillingExposureAdmission.Admit(ctx, f3rbAdmissionInput(callID.String(), accountID))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	closure := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID,
		ALegID: "a-f3rb", SessionID: "sess-f3rb",
		StartedAt: time.Unix(200, 0).UTC(), FinishedAt: time.Unix(201, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: exp.PricingRef, ChargePolicyRef: exp.ChargePolicyRef,
		ExpectedBLegIDs: []string{"b-r4"},
	}
	if err := prod.BillingTerminalUsageSink.AppendCall(ctx, closure); err != nil {
		t.Fatalf("append call: %v", err)
	}
	leg := billing.CallLegUsageRecord{
		CallID: callID, ALegID: "a-f3rb", BLegID: "b-r4", AttemptSeq: 1,
		BackendID: "openai-responses", ProviderID: "p", ModelID: "gpt",
		StartedAt: time.Unix(200, 0).UTC(), FinishedAt: time.Unix(200, 500000000).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens: billing.Quantity{Value: 5, Present: true}, OutputTokens: billing.Quantity{Value: 2, Present: true},
			Cost:      billing.MoneyEvidence{NanoUnits: 11, Currency: "USD", Present: true},
			Source:    billing.EvidenceSourceProviderReported,
			Authority: billing.EvidenceAuthorityAuthoritative, DedupeKey: "r4-rt-1",
		},
		OperatorRateRef: billing.VersionRef{ID: "operator-rates", Version: "v1"},
	}
	if err := prod.BillingTerminalUsageSink.AppendLeg(ctx, leg); err != nil {
		t.Fatalf("append leg: %v", err)
	}
	// Production worker construction with the drop-token decorator as the
	// mandatory claim port (composition accepts the interface).
	custWorker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, r4RatingStub{charge: 90, fp: "r4-rt-fp", call: callID}, r4DropCustomerClaimer{inner: store}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := custWorker.ProcessOnce(ctx); err == nil {
		t.Fatalf("R4 runtime: dropped customer token must fail closed, got nil")
	}
	txs, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tx := range txs {
		if tx.OperationKind == "customer_call_settlement" {
			t.Fatalf("R4 runtime: dropped token posted customer journal, want 0")
		}
	}
	provWorker, err := billing.NewCallProviderCostWorkerWithCutover(store, store, r4ProviderStub{}, r4DropProviderClaimer{inner: store}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := provWorker.ProcessOnce(ctx); err == nil {
		t.Fatalf("R4 runtime: dropped provider token must fail closed, got nil")
	}
	txs2, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tx := range txs2 {
		if tx.OperationKind == "provider_call_cogs" {
			t.Fatalf("R4 runtime: dropped provider token posted journal, want 0")
		}
	}
}
