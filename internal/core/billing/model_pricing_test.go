package billing

import (
	"errors"
	"testing"
	"time"
)

// Phase 2.2/2.3 — model-specific customer pricing flows through the internal
// call-rating input and every selected customer-billable B-leg is rated with
// its effective backend/model card:
//
//   - no model cards          -> configured default pricing applies;
//   - model cards present     -> each selected leg uses its own card before
//     summing, never an unrelated model or the default as a silent substitute;
//   - mixed-model failover    -> the surfaced (or charge-all) winner pays its
//     own effective card;
//   - missing applicable card -> rating fails explicitly (ErrRatingEvidenceMissing)
//     when any override context exists.
//
// The exact admitted-maximum contract (actual returned unclamped, settlement
// reconciles mismatches) is exercised by the existing RateCall tests.

func TestRateCallMixedModelChargeAllUsesEachLegsOwnCard(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	policy := ratingPolicy(ChargeAllPotentialLegs)
	pricing := ratingPricing()

	// model-a keeps the default card (input 100, fixed 3).
	// model-b is overridden in the catalog to input 1000/output 2000 and carries
	// no fixed charges. Each leg must be rated with its own card before summing.
	modelCard := PricingSnapshot{
		Ref: pricing.Ref, Currency: "USD",
		InputPerMillionNano: 1000, OutputPerMillionNano: 2000,
		InputRatePresent: true, OutputRatePresent: true,
	}

	legA := testLeg("b-a", SurfacedYes, 1_000_000, 0, MoneyEvidence{}, true)
	legA.ModelID = "model-a"
	legA.ALegID = "a-1"
	legA.CallID = callID
	legB := testLeg("b-b", SurfacedYes, 1_000_000, 0, MoneyEvidence{}, true)
	legB.ModelID = "model-b"
	legB.ALegID = "a-1"
	legB.CallID = callID

	call := CallUsageRecord{
		SchemaVersion: CurrentRecordSchemaVersion, CallID: callID, AccountID: "acct-1",
		ALegID: "a-1", ExpectedBLegIDs: []string{"b-a", "b-b"},
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: TurnOutcomeCompleted, CustomerPricingRef: pricing.Ref, ChargePolicyRef: policy.Ref,
	}

	result, err := RateCall(CallRatingInput{
		Call:              call,
		Legs:              []CallLegUsageRecord{legA, legB},
		MaxCustomerCharge: Money{Nano: 10000, Currency: "USD"},
		CustomerPricing:   pricing,
		CustomerPolicy:    policy,
		ModelPricing: []ModelCustomerPricing{
			{BackendID: "backend", ModelID: "model-a", Pricing: pricing},
			{BackendID: "backend", ModelID: "model-b", Pricing: modelCard},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// model-a: 1,000,000*100/1e6 + 3 fixed = 103; model-b: 1,000,000*1000/1e6 = 1000.
	if got, want := result.CustomerCharge.Nano, int64(1103); got != want {
		t.Fatalf("mixed-model customer = %d, want %d (each leg must use its own card before summing)", got, want)
	}
}

func TestRateCallMixedModelFailoverSettlesExpensiveWinner(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	policy := ratingPolicy(ChargeSurfacedTurn)
	pricing := ratingPricing()

	// Cheap attempt 1 (model-a) fails and is not surfaced. The surfaced winner
	// is the expensive failover model-b; settlement must use model-b's card.
	cheap := testLeg("b_z7x9p", SurfacedNo, 1_000_000, 0, MoneyEvidence{}, true)
	cheap.ModelID = "model-a"
	cheap.AttemptSeq = 1
	cheap.ALegID = "a-1"
	cheap.CallID = callID

	expensive := testLeg("b_a1b2c", SurfacedYes, 1_000_000, 0, MoneyEvidence{}, true)
	expensive.ModelID = "model-b"
	expensive.AttemptSeq = 2
	expensive.ALegID = "a-1"
	expensive.CallID = callID

	modelCard := PricingSnapshot{
		Ref: pricing.Ref, Currency: "USD",
		InputPerMillionNano: 1000, OutputPerMillionNano: 2000,
		InputRatePresent: true, OutputRatePresent: true,
	}

	call := CallUsageRecord{
		SchemaVersion: CurrentRecordSchemaVersion, CallID: callID, AccountID: "acct-1",
		ALegID: "a-1", ExpectedBLegIDs: []string{"b_a1b2c", "b_z7x9p"},
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: TurnOutcomeCompleted, CustomerPricingRef: pricing.Ref, ChargePolicyRef: policy.Ref,
	}

	result, err := RateCall(CallRatingInput{
		Call:              call,
		Legs:              []CallLegUsageRecord{cheap, expensive},
		MaxCustomerCharge: Money{Nano: 10000, Currency: "USD"},
		CustomerPricing:   pricing,
		CustomerPolicy:    policy,
		ModelPricing: []ModelCustomerPricing{
			{BackendID: "backend", ModelID: "model-a", Pricing: pricing},
			{BackendID: "backend", ModelID: "model-b", Pricing: modelCard},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Only the surfaced model-b leg is billed at model-b's effective card.
	if got, want := result.CustomerCharge.Nano, int64(1000); got != want {
		t.Fatalf("failover customer = %d, want %d (settle expensive winner with its own card)", got, want)
	}
}

func TestRateCallMissingApplicableModelCardFailsExplicitly(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	policy := ratingPolicy(ChargeAllPotentialLegs)
	pricing := ratingPricing()

	legA := testLeg("b-a", SurfacedYes, 1_000_000, 0, MoneyEvidence{}, true)
	legA.ModelID = "model-a"
	legA.ALegID = "a-1"
	legA.CallID = callID
	legB := testLeg("b-b", SurfacedYes, 1_000_000, 0, MoneyEvidence{}, true)
	legB.ModelID = "model-b"
	legB.ALegID = "a-1"
	legB.CallID = callID

	call := CallUsageRecord{
		SchemaVersion: CurrentRecordSchemaVersion, CallID: callID, AccountID: "acct-1",
		ALegID: "a-1", ExpectedBLegIDs: []string{"b-a", "b-b"},
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: TurnOutcomeCompleted, CustomerPricingRef: pricing.Ref, ChargePolicyRef: policy.Ref,
	}

	// A route/model override context exists (model-a card supplied) but the
	// selected model-b leg has no applicable card. Rating must fail explicitly;
	// it must not silently fall back to the default or an unrelated model price.
	result, err := RateCall(CallRatingInput{
		Call:              call,
		Legs:              []CallLegUsageRecord{legA, legB},
		MaxCustomerCharge: Money{Nano: 10000, Currency: "USD"},
		CustomerPricing:   pricing,
		CustomerPolicy:    policy,
		ModelPricing: []ModelCustomerPricing{
			{BackendID: "backend", ModelID: "model-a", Pricing: pricing},
		},
	})
	if !errors.Is(err, ErrRatingEvidenceMissing) {
		t.Fatalf("missing applicable model card = err %v (result %+v), want ErrRatingEvidenceMissing", err, result)
	}
}

func TestRateCallDefaultPricingWhenNoModelCards(t *testing.T) {
	t.Parallel()
	policy := ratingPolicy(ChargeSurfacedTurn)
	leg := testLeg("b-1", SurfacedYes, 1_000_000, 1_000_000, MoneyEvidence{}, true)
	leg.AttemptSeq = 1
	// No ModelPricing at all: default pricing applies (input 100 + output 200 + fixed 3).
	result := rateCallFromLegs(t, TurnOutcomeCompleted, []CallLegUsageRecord{leg}, policy, 1000)
	if got, want := result.CustomerCharge.Nano, int64(303); got != want {
		t.Fatalf("default-pricing customer = %d, want %d", got, want)
	}
}
