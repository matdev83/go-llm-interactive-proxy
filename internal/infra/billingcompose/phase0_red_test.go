package billingcompose_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
)

func TestResolveCallRating_MixedModelPricingKeepsOverrides(t *testing.T) {
	// Cover two customer-billable model legs with distinct effective cards so
	// settlement remains aligned with route-specific admission pricing.
	t.Parallel()

	c, pricing, policy, rates := seedCatalog(t)

	// Create a policy that charges all legs, but excludes fixed charges for simpler math.
	policyAll := policy
	policyAll.Ref = billing.VersionRef{ID: "policy", Version: "v10"}
	policyAll.Scope = billing.ChargeAllPotentialLegs
	policyAll.IncludeFixedCharges = false
	if err := c.PutPolicy(policyAll); err != nil {
		t.Fatal(err)
	}

	// Create an override pricing snapshot for "backend-special-1/model-special-1"
	// with input pricing ($500/million vs default $100/million).
	override1 := pricing
	override1.Ref = billing.VersionRef{ID: "pricing-override-1", Version: "v1"}
	override1.InputPerMillionNano = 500
	override1.OutputPerMillionNano = 1000
	override1.InputRatePresent = true
	override1.OutputRatePresent = true
	override1.FixedCharges = nil
	if err := c.PutPricing(override1); err != nil {
		t.Fatal(err)
	}
	if err := c.SetRoutePricing("backend-special-1", "model-special-1", override1.Ref); err != nil {
		t.Fatal(err)
	}

	// Create an override pricing snapshot for "backend-special-2/model-special-2"
	// with input pricing ($300/million vs default $100/million).
	override2 := pricing
	override2.Ref = billing.VersionRef{ID: "pricing-override-2", Version: "v1"}
	override2.InputPerMillionNano = 300
	override2.OutputPerMillionNano = 600
	override2.InputRatePresent = true
	override2.OutputRatePresent = true
	override2.FixedCharges = nil
	if err := c.PutPricing(override2); err != nil {
		t.Fatal(err)
	}
	if err := c.SetRoutePricing("backend-special-2", "model-special-2", override2.Ref); err != nil {
		t.Fatal(err)
	}

	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}

	// 1. Admission: Prove that the admission Quote sees the route-specific override prices.
	admPricing1, err := c.RoutePricing(context.Background(), "backend-special-1", "model-special-1")
	if err != nil {
		t.Fatal(err)
	}
	if admPricing1.InputPerMillionNano != 500 {
		t.Fatalf("admission pricing 1 = %d, want 500", admPricing1.InputPerMillionNano)
	}

	admPricing2, err := c.RoutePricing(context.Background(), "backend-special-2", "model-special-2")
	if err != nil {
		t.Fatal(err)
	}
	if admPricing2.InputPerMillionNano != 300 {
		t.Fatalf("admission pricing 2 = %d, want 300", admPricing2.InputPerMillionNano)
	}

	// 2. Settlement: Build a complete call using both models.
	// Leg 1: backend-special-1/model-special-1, 1,000,000 input tokens.
	leg1 := billing.CallLegUsageRecord{
		CallID:     callID,
		ALegID:     "a-1",
		BLegID:     "b-1",
		BackendID:  "backend-special-1",
		ProviderID: "provider",
		ModelID:    "model-special-1",
		StartedAt:  time.Unix(100, 0).UTC(),
		FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:    billing.LegOutcomeWinner,
		Surfaced:   billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens:  billing.Quantity{Value: 1_000_000, Present: true},
			OutputTokens: billing.Quantity{Value: 0, Present: true},
			Source:       billing.EvidenceSourceProviderReported,
			Authority:    billing.EvidenceAuthorityAuthoritative,
		},
		OperatorRateRef: rates.Ref,
	}
	sealedLeg1, err := leg1.Seal()
	if err != nil {
		t.Fatal(err)
	}

	// Leg 2: backend-special-2/model-special-2, 2,000,000 input tokens.
	leg2 := billing.CallLegUsageRecord{
		CallID:     callID,
		ALegID:     "a-1",
		BLegID:     "b-2",
		BackendID:  "backend-special-2",
		ProviderID: "provider",
		ModelID:    "model-special-2",
		StartedAt:  time.Unix(100, 0).UTC(),
		FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:    billing.LegOutcomeWinner,
		Surfaced:   billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens:  billing.Quantity{Value: 2_000_000, Present: true},
			OutputTokens: billing.Quantity{Value: 0, Present: true},
			Source:       billing.EvidenceSourceProviderReported,
			Authority:    billing.EvidenceAuthorityAuthoritative,
		},
		OperatorRateRef: rates.Ref,
	}
	sealedLeg2, err := leg2.Seal()
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
		ChargePolicyRef:    policyAll.Ref,
		ExpectedBLegIDs:    []string{"b-1", "b-2"},
	}
	sealedClosure, err := closure.Seal()
	if err != nil {
		t.Fatal(err)
	}

	resolver, err := billingcompose.NewCallRatingResolver(c)
	if err != nil {
		t.Fatal(err)
	}

	complete := billing.CompleteCall{
		Closure: sealedClosure,
		Legs:    []billing.CallLegUsageRecord{sealedLeg1, sealedLeg2},
	}

	exposure := billing.CallExposure{
		CallID: callID.String(),
		Max:    billing.Money{Nano: 10000, Currency: "USD"},
	}

	result, err := resolver.ResolveCallRating(context.Background(), complete, exposure)
	if err != nil {
		t.Fatal(err)
	}

	// Leg 1 uses override1: 1,000,000 * 500 / 1,000,000 = 500 nano.
	// Leg 2 uses override2: 2,000,000 * 300 / 1,000,000 = 600 nano.
	if got, want := result.CustomerCharge.Nano, int64(1100); got != want {
		t.Errorf("CustomerCharge = %d, want %d", got, want)
	}
}

func TestResolveCallRating_MissingOperatorRateDoesNotBlockCustomerRating(t *testing.T) {
	t.Parallel()

	c, pricing, policy, _ := seedCatalog(t)

	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}

	// Leg references an operator rate version "missing" which does NOT exist in the catalog
	missingRateRef := billing.VersionRef{ID: "operator-rates", Version: "missing"}

	leg := billing.CallLegUsageRecord{
		CallID:     callID,
		ALegID:     "a-1",
		BLegID:     "b-1",
		BackendID:  "backend",
		ProviderID: "provider",
		ModelID:    "model",
		StartedAt:  time.Unix(100, 0).UTC(),
		FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:    billing.LegOutcomeWinner,
		Surfaced:   billing.SurfacedYes,
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

	resolver, err := billingcompose.NewCallRatingResolver(c)
	if err != nil {
		t.Fatal(err)
	}

	complete := billing.CompleteCall{
		Closure: sealedClosure,
		Legs:    []billing.CallLegUsageRecord{sealedLeg},
	}

	exposure := billing.CallExposure{
		CallID: callID.String(),
		Max:    billing.Money{Nano: 10000, Currency: "USD"},
	}

	// Customer rating resolves only customer pricing/policy/model cards and is
	// independent of provider-cost readiness: the missing operator-rate ref must
	// not block settlement (Phase 2 split).
	result, err := resolver.ResolveCallRating(context.Background(), complete, exposure)
	if err != nil {
		t.Errorf("ResolveCallRating failed: %v", err)
		return
	}

	// The customer charge should be computed successfully (1,000,000 * 100 + 3 = 103 nano).
	if got, want := result.CustomerCharge.Nano, int64(103); got != want {
		t.Errorf("CustomerCharge = %d, want %d", got, want)
	}
}
