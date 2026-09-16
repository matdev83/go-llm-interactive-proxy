package billing

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestRefinement43V1ScalarExplicitRetailModeOverridesContradictoryLegacyScope(t *testing.T) {
	t.Parallel()
	policy := ratingPolicy(ChargeAllPotentialLegs)
	policy.Retail = &RetailSelectionPolicy{
		Mode:  RetailSelectionSurfacedWinner,
		Basis: RetailBasisIndependent,
	}
	legs := []CallLegUsageRecord{
		testLeg("b-retry", SurfacedNo, 10_000_000, 0, MoneyEvidence{}, true),
		testLeg("b-winner", SurfacedYes, 1_000_000, 0, MoneyEvidence{}, true),
	}
	legs[0].Outcome = LegOutcomeFailed
	legs[1].Outcome = LegOutcomeWinner
	result := rateCallFromLegs(t, TurnOutcomeCompleted, legs, policy, 100_000)
	if got, want := result.CustomerCharge.Nano, int64(103); got != want {
		t.Fatalf("customer charge = %d, want only canonical surfaced winner charge %d", got, want)
	}
}

func TestRefinement43V1ScalarSurfacedWinnerFallsBackToUniqueOutcomeWinner(t *testing.T) {
	t.Parallel()
	policy := ratingPolicy(ChargeSurfacedTurn)
	leg := testLeg("b-winner", SurfacedNo, 1_000_000, 0, MoneyEvidence{}, true)
	leg.Outcome = LegOutcomeWinner
	result := rateCallFromLegs(t, TurnOutcomeCompleted, []CallLegUsageRecord{leg}, policy, 100_000)
	if got, want := result.CustomerCharge.Nano, int64(103); got != want {
		t.Fatalf("customer charge = %d, want unique unsurfaced outcome winner charge %d", got, want)
	}
}

func TestRefinement43V1ScalarRejectsMultipleSurfacedCandidates(t *testing.T) {
	t.Parallel()
	policy := ratingPolicy(ChargeSurfacedTurn)
	legs := []CallLegUsageRecord{
		testLeg("b-one", SurfacedYes, 1_000_000, 0, MoneyEvidence{}, true),
		testLeg("b-two", SurfacedYes, 2_000_000, 0, MoneyEvidence{}, true),
	}
	for i := range legs {
		legs[i].Outcome = LegOutcomeWinner
	}
	call, legs := refinement43CallWithLegs(t, TurnOutcomeCompleted, legs, policy, ratingPricing())
	_, err := RateCall(CallRatingInput{
		Call: call, Legs: legs, MaxCustomerCharge: Money{Nano: 100_000, Currency: "USD"},
		CustomerPricing: ratingPricing(), CustomerPolicy: policy,
	})
	if !errors.Is(err, ErrRetailSelectionAmbiguous) {
		t.Fatalf("multiple surfaced scalar rating error = %v, want %v", err, ErrRetailSelectionAmbiguous)
	}
}

func TestRefinement43V1AndV2ShareFrozenRetailSelectionAndTotal(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
	policy.IncludeFixedCharges = true
	pricing := PricingSnapshot{
		Ref:                 policy.PricingRef,
		Currency:            "USD",
		InputPerMillionNano: 100_000_000_000,
		InputRatePresent:    true,
		FixedCharges: []ChargeComponent{{
			Name: "call-fee", Amount: Money{Nano: 3_000_000_000, Currency: "USD"},
		}},
	}

	v1Leg := testLeg("b-winner", SurfacedNo, 1_000_000, -1, MoneyEvidence{}, true)
	v1Leg.Outcome = LegOutcomeWinner
	v1Call, v1Legs := refinement43CallWithLegs(t, TurnOutcomeCompleted, []CallLegUsageRecord{v1Leg}, policy, pricing)
	v1Selected, err := SelectRetailBLegs(v1Legs, v1Call.Outcome)
	if err != nil {
		t.Fatalf("V1 selection: %v", err)
	}

	key := metering.ComponentKey{
		Direction: metering.DirectionInput, Component: metering.ComponentTextToken,
		Unit: metering.UnitToken, SchemaID: "retail.v1",
	}
	v2Call := retailSelectionCall(t, policy, "b-winner")
	v2Call.Outcome = TurnOutcomeCompleted
	v2Leg := phase10RetailLeg(t, v2Call.CallID, "b-winner", 1, LegOutcomeWinner, SurfacedNo,
		phase10RetailObservation(t, v2Call.CallID, "b-winner", "winner-usage", metering.OriginProvider, metering.BoundaryBackendIngress,
			phase10RetailMeasure{key: key, quantity: "1"},
		))
	v2Selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: v2Call, Legs: []CallLegUsageRecord{v2Leg}, Policy: policy})
	if err != nil {
		t.Fatalf("V2 selection: %v", err)
	}
	if len(v1Selected) != 1 || len(v2Selection.SelectedBLegs) != 1 || v1Selected[0].BLegID != v2Selection.SelectedBLegs[0].BLegID {
		t.Fatalf("V1/V2 selected legs differ: V1=%+v V2=%+v", v1Selected, v2Selection.SelectedBLegs)
	}

	v1, err := RateCall(CallRatingInput{
		Call: v1Call, Legs: v1Legs, MaxCustomerCharge: Money{Nano: 1_000_000_000_000, Currency: "USD"},
		CustomerPricing: pricing, CustomerPolicy: policy,
	})
	if err != nil {
		t.Fatalf("V1 rating: %v", err)
	}
	tariff := phase10RetailTariff(t, []economics.RatingRule{
		phase10RetailLinearRule("text-input", key, "100"),
		phase10RetailFixedRule("call-fee", economics.FixedFeeScopeCall, "3"),
	})
	v2, err := RateCall(CallRatingInput{
		Call: v2Call, Legs: []CallLegUsageRecord{v2Leg}, MaxCustomerCharge: Money{Nano: 1_000_000_000_000, Currency: "USD"},
		CustomerPricing: pricing, CustomerPolicy: policy, CustomerTariff: tariff,
	})
	if err != nil {
		t.Fatalf("V2 rating: %v", err)
	}
	if v1.CustomerCharge != v2.CustomerCharge {
		t.Fatalf("V1/V2 equivalent selected total differs: V1=%+v V2=%+v", v1.CustomerCharge, v2.CustomerCharge)
	}
}

func refinement43CallWithLegs(t *testing.T, outcome TurnOutcome, legs []CallLegUsageRecord, policy ChargePolicy, pricing PricingSnapshot) (CallUsageRecord, []CallLegUsageRecord) {
	t.Helper()
	callID := mustBillingCallID(t)
	base := testCallUsageRecord(callID)
	ids := make([]string, 0, len(legs))
	for i := range legs {
		legs[i].CallID = callID
		legs[i].ALegID = "a-1"
		ids = append(ids, legs[i].BLegID)
	}
	call := CallUsageRecord{
		SchemaVersion: CurrentRecordSchemaVersion,
		CallID:        callID, AccountID: "acct-1", ALegID: "a-1", ExpectedBLegIDs: ids,
		StartedAt: base.StartedAt, FinishedAt: base.FinishedAt,
		Outcome: outcome, CustomerPricingRef: pricing.Ref, ChargePolicyRef: policy.Ref,
	}
	return call, legs
}
