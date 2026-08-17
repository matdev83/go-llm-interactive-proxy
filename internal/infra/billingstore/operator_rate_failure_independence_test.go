package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
)

// Phase 2.4 — customer settlement and exposure close must succeed even when
// one or more operator-rate lookups fail. Provider-cost work then remains
// pending/unreconciled and never alters the customer posting.

func TestSQLiteCustomerSettlementClosesWhileOperatorRateLookupFails(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	account := billing.Account{ID: "op-rate-fail-customer", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}

	// Catalog publishes customer pricing/policy defaults only. No operator rate
	// is published for the legacy operator-rates refs, and no route/model
	// pricing overrides exist.
	pricing := billing.PricingSnapshot{
		Ref:                  billing.VersionRef{ID: "prices", Version: "v1"},
		Currency:             "USD",
		InputPerMillionNano:  100,
		OutputPerMillionNano: 200,
		InputRatePresent:     true,
		OutputRatePresent:    true,
		FixedCharges:         []billing.ChargeComponent{{Name: "request", Amount: billing.Money{Nano: 3, Currency: "USD"}}},
	}
	policy := billing.ChargePolicy{
		Ref:                 billing.VersionRef{ID: "policy", Version: "v2"},
		PricingRef:          pricing.Ref,
		Scope:               billing.ChargeSurfacedTurn,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
		IncludeFixedCharges: true,
	}
	c := billingcompose.NewSnapshotCatalog()
	if err := c.PutPricing(pricing); err != nil {
		t.Fatal(err)
	}
	if err := c.PutPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDefaults(pricing.Ref, policy.Ref); err != nil {
		t.Fatal(err)
	}

	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID,
		ALegID: "a-shared", SessionID: "sess-1", StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.TurnOutcomeCompleted, CustomerPricingRef: pricing.Ref, ChargePolicyRef: policy.Ref,
		ExpectedBLegIDs: []string{"b-1"},
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}

	// The leg carries token evidence (so provider cost requires an operator
	// rate) but references an operator-rate version that does not exist in the
	// catalog. Customer rating must be wholly unaffected.
	leg := billing.CallLegUsageRecord{
		CallID: callID, ALegID: "a-shared", BLegID: "b-1", AttemptSeq: 1,
		BackendID: "backend-a", ProviderID: "provider-a", ModelID: "model-a",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(100, 500000000).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens:  billing.Quantity{Value: 1_000_000, Present: true},
			OutputTokens: billing.Quantity{Value: 0, Present: true},
			Source:       billing.EvidenceSourceProviderReported,
			Authority:    billing.EvidenceAuthorityAuthoritative,
		},
		OperatorRateRef: billing.VersionRef{ID: "operator-rates", Version: "missing"},
	}
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}

	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 1000, Currency: "USD"},
		PricingRef: pricing.Ref, ChargePolicyRef: policy.Ref,
	})
	if err != nil {
		t.Fatal(err)
	}

	sealedClosure, err := call.Seal()
	if err != nil {
		t.Fatal(err)
	}
	sealedLeg, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}

	// 1. Customer settlement through the real compose resolver: must succeed
	// with default pricing (1,000,000 input * 100/million + 3 fixed = 103).
	resolver, err := billingcompose.NewCallRatingResolver(c)
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolver.ResolveCallRating(ctx, billing.CompleteCall{Closure: sealedClosure, Legs: []billing.CallLegUsageRecord{sealedLeg}}, exposure)
	if err != nil {
		t.Fatalf("customer rating failed while operator rate is missing: %v", err)
	}
	if got, want := result.CustomerCharge.Nano, int64(103); got != want {
		t.Fatalf("customer charge = %d, want %d", got, want)
	}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: sealedClosure, Exposure: exposure, Result: result}); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 897 {
		t.Fatalf("balance after customer settlement = %d, want 897", got.BalanceNano)
	}
	var status string
	if err := store.db.NewRaw(`SELECT status FROM call_exposures WHERE call_id = ?`, callID.String()).Scan(ctx, &status); err != nil {
		t.Fatal(err)
	}
	if status != "closed" {
		t.Fatalf("exposure status = %q, want closed after customer settlement despite missing operator rate", status)
	}

	// 2. Provider-cost resolution for the same leg fails closed independently.
	providerResolver, err := billingcompose.NewProviderCostResolver(c, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := providerResolver.ResolveProviderCost(ctx, sealedLeg); !errors.Is(err, billing.ErrUnreconciledCost) {
		t.Fatalf("provider cost = err %v, want ErrUnreconciledCost", err)
	}

	// 3. Provider-cost work stays pending and must not touch the settled
	// customer account or reopen exposure.
	pending, err := store.ListPendingProviderCostWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending provider-cost work = %d, want 1 (unreconciled work remains queued)", len(pending))
	}
	if pending[0].Leg.OperatorRateRef != leg.OperatorRateRef {
		t.Fatalf("pending work leg = %+v, want original operator rate ref %+v", pending[0].Leg, leg.OperatorRateRef)
	}
	after, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.BalanceNano != 897 || after.Version != got.Version {
		t.Fatalf("provider-cost failure altered customer posting: before=%+v after=%+v", got, after)
	}
	if err := store.db.NewRaw(`SELECT status FROM call_exposures WHERE call_id = ?`, callID.String()).Scan(ctx, &status); err != nil {
		t.Fatal(err)
	}
	if status != "closed" {
		t.Fatalf("exposure status = %q, want still closed after failed provider-cost lookup", status)
	}
}
