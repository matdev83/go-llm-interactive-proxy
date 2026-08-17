package billingcompose_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
)

// Phase 2.1/2.3/2.4 — customer snapshot resolution is split from
// provider/operator-rate resolution, and every selected customer-billable
// B-leg is rated with its effective backend/model card.

func TestCustomerRatingSnapshotsIgnoresOperatorRates(t *testing.T) {
	t.Parallel()
	c, pricing, policy, _ := seedCatalog(t)
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	missingRateRef := billing.VersionRef{ID: "operator-rates", Version: "missing"}
	leg := billing.CallLegUsageRecord{
		CallID: callID, ALegID: "a-1", BLegID: "b-1",
		BackendID: "backend", ProviderID: "provider", ModelID: "model",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens:  billing.Quantity{Value: 1_000_000, Present: true},
			OutputTokens: billing.Quantity{Value: 0, Present: true},
			Source:       billing.EvidenceSourceProviderReported,
			Authority:    billing.EvidenceAuthorityAuthoritative,
		},
		OperatorRateRef: missingRateRef,
	}
	sealedLeg, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	closure := billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             callID,
		AccountID:          "acct-1",
		ALegID:             "a-1",
		StartedAt:          time.Unix(100, 0).UTC(),
		FinishedAt:         time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: pricing.Ref,
		ChargePolicyRef:    policy.Ref,
		ExpectedBLegIDs:    []string{"b-1"},
	}
	sealedClosure, err := closure.Seal()
	if err != nil {
		t.Fatal(err)
	}

	// The customer path must succeed even though the persisted OperatorRateRef
	// has no published rate: customer rating needs pricing/policy/model cards
	// only and must never eagerly load operator rates.
	got, err := c.CustomerRatingSnapshots(sealedClosure, []billing.CallLegUsageRecord{sealedLeg})
	if err != nil {
		t.Fatalf("CustomerRatingSnapshots failed on missing operator rate: %v (customer rating must be independent of provider-cost readiness)", err)
	}
	assertPricingEqual(t, got.DefaultPricing, pricing)
	assertPolicyEqual(t, got.Policy, policy)
	if len(got.ModelPricing) != 0 {
		t.Fatalf("unexpected model cards for unoverridden route: %+v", got.ModelPricing)
	}

	// The provider path is independently per-leg and fails closed when its own
	// operator rate is missing: it must not preclude or alter the customer path.
	providerResolver, err := billingcompose.NewProviderCostResolver(c, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := providerResolver.ResolveProviderCost(context.Background(), sealedLeg); !errors.Is(err, billing.ErrUnreconciledCost) {
		t.Fatalf("provider cost = err %v, want ErrUnreconciledCost (missing operator rate)", err)
	}
}

func TestResolveCallRatingFailoverSettlesSurfacedModelCard(t *testing.T) {
	t.Parallel()
	c, pricing, policy, _ := seedCatalog(t)

	// A route/model pricing override exists for the failover model; admission
	// quotes with it and settlement must use the same effective card.
	override := pricing
	override.Ref = billing.VersionRef{ID: "pricing-failover", Version: "v1"}
	override.InputPerMillionNano = 1000
	override.OutputPerMillionNano = 2000
	override.InputRatePresent = true
	override.OutputRatePresent = true
	override.FixedCharges = nil
	if err := c.PutPricing(override); err != nil {
		t.Fatal(err)
	}
	if err := c.SetRoutePricing("backend-special", "model-special", override.Ref); err != nil {
		t.Fatal(err)
	}

	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	// Cheap attempt 1 (no override route) fails and is not surfaced.
	cheap := billing.CallLegUsageRecord{
		CallID: callID, ALegID: "a-1", BLegID: "b_z7x9p", AttemptSeq: 1,
		BackendID: "backend", ProviderID: "provider", ModelID: "model",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.LegOutcomeFailed, Surfaced: billing.SurfacedNo,
		Evidence: billing.FinalBillingEvidence{
			InputTokens:  billing.Quantity{Value: 1_000_000, Present: true},
			OutputTokens: billing.Quantity{Value: 0, Present: true},
			Source:       billing.EvidenceSourceProviderReported,
			Authority:    billing.EvidenceAuthorityAuthoritative,
		},
	}
	sealedCheap, err := cheap.Seal()
	if err != nil {
		t.Fatal(err)
	}
	// Expensive failover attempt 2 is the surfaced winner and must be billed
	// with its override card (input 1000), never the default (input 100).
	winner := billing.CallLegUsageRecord{
		CallID: callID, ALegID: "a-1", BLegID: "b_a1b2c", AttemptSeq: 2,
		BackendID: "backend-special", ProviderID: "provider", ModelID: "model-special",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens:  billing.Quantity{Value: 1_000_000, Present: true},
			OutputTokens: billing.Quantity{Value: 0, Present: true},
			Source:       billing.EvidenceSourceProviderReported,
			Authority:    billing.EvidenceAuthorityAuthoritative,
		},
	}
	sealedWinner, err := winner.Seal()
	if err != nil {
		t.Fatal(err)
	}

	closure := billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             callID,
		AccountID:          "acct-1",
		ALegID:             "a-1",
		StartedAt:          time.Unix(100, 0).UTC(),
		FinishedAt:         time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: pricing.Ref,
		ChargePolicyRef:    policy.Ref,
		ExpectedBLegIDs:    []string{"b_a1b2c", "b_z7x9p"},
	}
	sealedClosure, err := closure.Seal()
	if err != nil {
		t.Fatal(err)
	}

	resolver, err := billingcompose.NewCallRatingResolver(c)
	if err != nil {
		t.Fatal(err)
	}
	complete := billing.CompleteCall{Closure: sealedClosure, Legs: []billing.CallLegUsageRecord{sealedCheap, sealedWinner}}
	exposure := billing.CallExposure{CallID: callID.String(), Max: billing.Money{Nano: 10000, Currency: "USD"}}
	result, err := resolver.ResolveCallRating(context.Background(), complete, exposure)
	if err != nil {
		t.Fatal(err)
	}
	// Surfaced winner: 1,000,000 input at override 1000/million = 1000.
	if got, want := result.CustomerCharge.Nano, int64(1000); got != want {
		t.Errorf("failover settlement = %d, want %d (settle surfaced winner with its own model card)", got, want)
	}
}
