package billing

import (
	"errors"
	"testing"
	"time"
)

func ratingPricing() PricingSnapshot {
	return PricingSnapshot{
		Ref: VersionRef{ID: "pricing", Version: "v7"}, Currency: "USD",
		InputPerMillionNano: 100, OutputPerMillionNano: 200,
		InputRatePresent: true, OutputRatePresent: true,
		FixedCharges: []ChargeComponent{{Name: "request", Amount: Money{Nano: 3, Currency: "USD"}}},
	}
}

func ratingPolicy(scope ChargePolicyScope) ChargePolicy {
	return ChargePolicy{
		Ref: VersionRef{ID: "policy", Version: "v2"}, PricingRef: ratingPricing().Ref,
		Scope: scope, IncludeInputTokens: true, IncludeOutputTokens: true,
		IncludeFixedCharges: true,
	}
}

func operatorRate() OperatorRateSnapshot {
	return OperatorRateSnapshot{
		Ref: VersionRef{ID: "operator-rates", Version: "v1"}, Currency: "USD",
		InputPerMillionNano: 50, OutputPerMillionNano: 75,
		InputRatePresent: true, OutputRatePresent: true,
	}
}

func rateCallFromLegs(t *testing.T, outcome TurnOutcome, legs []CallLegUsageRecord, policy ChargePolicy, maxNano int64) CallRatingResult {
	t.Helper()
	callID := mustBillingCallID(t)
	ids := make([]string, 0, len(legs))
	for i := range legs {
		legs[i].CallID = callID
		legs[i].ALegID = "a-1"
		ids = append(ids, legs[i].BLegID)
	}
	call := CallUsageRecord{
		SchemaVersion: CurrentRecordSchemaVersion, CallID: callID, AccountID: "acct-1",
		ALegID: "a-1", ExpectedBLegIDs: ids, StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: outcome, CustomerPricingRef: ratingPricing().Ref, ChargePolicyRef: policy.Ref,
	}
	result, err := RateCall(CallRatingInput{
		Call: call, Legs: legs, MaxCustomerCharge: Money{Nano: maxNano, Currency: "USD"},
		CustomerPricing: ratingPricing(), CustomerPolicy: policy,
	})
	if err != nil {
		t.Fatalf("RateCall: %v", err)
	}
	return result
}

func testLeg(bLegID string, surfaced SurfacedState, in, out int64, cost MoneyEvidence, accepted bool) CallLegUsageRecord {
	ev := FinalBillingEvidence{}
	if accepted {
		if in >= 0 {
			ev.InputTokens = Quantity{Value: in, Present: true}
		}
		if out >= 0 {
			ev.OutputTokens = Quantity{Value: out, Present: true}
		}
	}
	if cost.Present {
		ev.Cost = cost
		ev.Authority = EvidenceAuthorityAuthoritative
		ev.Source = EvidenceSourceProviderReported
	}
	return CallLegUsageRecord{
		BLegID: bLegID, BackendID: "backend", ProviderID: "provider", ModelID: "model",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: LegOutcomeWinner, Surfaced: surfaced, Evidence: ev,
		OperatorRateRef: VersionRef{ID: "operator-rates", Version: "v1"},
	}
}

func TestRateCallChargesSurfacedTurn(t *testing.T) {
	t.Parallel()
	policy := ratingPolicy(ChargeSurfacedTurn)
	result := rateCallFromLegs(t, TurnOutcomeCompleted, []CallLegUsageRecord{
		testLeg("b-1", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{NanoUnits: 10, Currency: "USD", Present: true}, true),
		testLeg("b-2", SurfacedYes, 1_000_000, 1_000_000, MoneyEvidence{NanoUnits: 0, Currency: "USD", Present: true}, true),
	}, policy, 1000)
	// surfaced leg: 100+200 input/output + 3 fixed = 303
	if result.CustomerCharge.Nano != 303 {
		t.Fatalf("customer = %d, want 303", result.CustomerCharge.Nano)
	}
}

func TestRateCallBillsObservedUsageOnFailureAndCancel(t *testing.T) {
	t.Parallel()
	policy := ratingPolicy(ChargeSurfacedTurn)
	for _, outcome := range []TurnOutcome{TurnOutcomeFailed, TurnOutcomeCanceled} {
		result := rateCallFromLegs(t, outcome, []CallLegUsageRecord{
			testLeg("b-1", SurfacedYes, 1_000_000, 1_000_000, MoneyEvidence{}, true),
		}, policy, 1000)
		if result.CustomerCharge.Nano != 303 {
			t.Fatalf("%s customer = %d, want 303", outcome, result.CustomerCharge.Nano)
		}
	}
}

func TestRateCallCancelWithUnsurfacedOutputStillBills(t *testing.T) {
	t.Parallel()
	leg := testLeg("b-1", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{}, true)
	result := rateCallFromLegs(t, TurnOutcomeCanceled, []CallLegUsageRecord{leg}, ratingPolicy(ChargeSurfacedTurn), 1000)
	if result.CustomerCharge.Nano != 303 {
		t.Fatalf("customer = %d, want 303", result.CustomerCharge.Nano)
	}
}

func TestRateCallSurfacedTurnInterruptBillsOneLogicalAcceptedLeg(t *testing.T) {
	t.Parallel()
	policy := ratingPolicy(ChargeSurfacedTurn)
	legs := []CallLegUsageRecord{
		testLeg("b-1", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{}, true),
		testLeg("b-2", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{}, true),
	}
	// Real B2BUA attempt sequence: b-1 is attempt 1, b-2 is attempt 2.
	legs[0].AttemptSeq = 1
	legs[1].AttemptSeq = 2
	result := rateCallFromLegs(t, TurnOutcomeCanceled, legs, policy, 1000)
	// latest Seq wins for interrupt without surfaced legs: one leg + fixed = 303
	if result.CustomerCharge.Nano != 303 {
		t.Fatalf("customer = %d, want 303", result.CustomerCharge.Nano)
	}
}

func TestRateCallAllPotentialLegsSumsAccepted(t *testing.T) {
	t.Parallel()
	policy := ratingPolicy(ChargeAllPotentialLegs)
	result := rateCallFromLegs(t, TurnOutcomeCompleted, []CallLegUsageRecord{
		testLeg("b-1", SurfacedYes, 1_000_000, 1_000_000, MoneyEvidence{}, true),
		testLeg("b-2", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{}, true),
	}, policy, 1000)
	// two legs: 2*(100+200) + 2*3 fixed = 606
	if result.CustomerCharge.Nano != 606 {
		t.Fatalf("customer = %d, want 606", result.CustomerCharge.Nano)
	}
}

func TestRateCallRejectedProviderAttemptIsZero(t *testing.T) {
	t.Parallel()
	leg := testLeg("b-1", SurfacedNo, -1, -1, MoneyEvidence{}, false)
	result := rateCallFromLegs(t, TurnOutcomeCompleted, []CallLegUsageRecord{leg}, ratingPolicy(ChargeAllPotentialLegs), 1000)
	if result.CustomerCharge.Nano != 0 {
		t.Fatalf("customer = %d, want 0", result.CustomerCharge.Nano)
	}
}

func TestRateCallCancelBillsInputOnlyWhenOutputAbsent(t *testing.T) {
	t.Parallel()
	leg := testLeg("b-1", SurfacedYes, 1_000_000, -1, MoneyEvidence{}, true)
	result := rateCallFromLegs(t, TurnOutcomeCanceled, []CallLegUsageRecord{leg}, ratingPolicy(ChargeSurfacedTurn), 1000)
	// input 100 + fixed 3 = 103
	if result.CustomerCharge.Nano != 103 {
		t.Fatalf("customer = %d, want 103", result.CustomerCharge.Nano)
	}
}

func TestRateCallReturnsActualWhenExceedsMax(t *testing.T) {
	t.Parallel()
	result := rateCallFromLegs(t, TurnOutcomeCompleted, []CallLegUsageRecord{
		testLeg("b-1", SurfacedYes, 1_000_000, 1_000_000, MoneyEvidence{}, true),
	}, ratingPolicy(ChargeSurfacedTurn), 1)
	if result.CustomerCharge.Nano <= 1 {
		t.Fatalf("customer = %d, want > admitted max", result.CustomerCharge.Nano)
	}
}

func TestRateCallInterruptedFailsClosedWhenIncludedRateMissing(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	pricing := ratingPricing()
	pricing.OutputRatePresent = false
	pricing.OutputPerMillionNano = 0
	policy := ratingPolicy(ChargeSurfacedTurn)
	leg := testLeg("b-1", SurfacedYes, 1_000_000, 1_000_000, MoneyEvidence{}, true)
	leg.CallID = callID
	leg.ALegID = "a-1"
	call := CallUsageRecord{
		SchemaVersion: CurrentRecordSchemaVersion, CallID: callID, AccountID: "acct-1",
		ALegID: "a-1", ExpectedBLegIDs: []string{"b-1"}, StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: TurnOutcomeCanceled, CustomerPricingRef: pricing.Ref, ChargePolicyRef: policy.Ref,
	}
	_, err := RateCall(CallRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, MaxCustomerCharge: Money{Nano: 1000, Currency: "USD"},
		CustomerPricing: pricing, CustomerPolicy: policy,
	})
	if !errors.Is(err, ErrRatingEvidenceMissing) {
		t.Fatalf("err = %v, want ErrRatingEvidenceMissing", err)
	}
}

func TestRateProviderCostAuthoritativeAndFallback(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	auth := testCallLegUsageRecord(callID, "b-auth")
	auth.Evidence.Cost = MoneyEvidence{NanoUnits: 11, Currency: "USD", Present: true}
	auth.Evidence.Authority = EvidenceAuthorityAuthoritative
	got, err := RateProviderCost(auth, nil, "USD")
	if err != nil || got.Amount.Nano != 11 || !got.Authoritative {
		t.Fatalf("authoritative = %+v err=%v", got, err)
	}

	fallback := testCallLegUsageRecord(callID, "b-fb")
	fallback.Evidence.Cost = MoneyEvidence{}
	fallback.OperatorRateRef = operatorRate().Ref
	got, err = RateProviderCost(fallback, OperatorRateSet{operatorRate()}, "USD")
	if err != nil || !got.Reconciled || got.Authoritative {
		t.Fatalf("fallback = %+v err=%v", got, err)
	}
}

func TestRateProviderCostUnreconciledWhenRateMissing(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	leg := testCallLegUsageRecord(callID, "b-1")
	leg.Evidence.Cost = MoneyEvidence{}
	leg.OperatorRateRef = VersionRef{ID: "missing", Version: "v1"}
	_, err := RateProviderCost(leg, OperatorRateSet{operatorRate()}, "USD")
	if !errors.Is(err, ErrUnreconciledCost) {
		t.Fatalf("err = %v, want ErrUnreconciledCost", err)
	}
}

func TestRateCallUsesPerModelCustomerCards(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	policy := ratingPolicy(ChargeAllPotentialLegs)
	pricing := ratingPricing()
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
		ALegID: "a-1", ExpectedBLegIDs: []string{"b-a", "b-b"}, StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: TurnOutcomeCompleted, CustomerPricingRef: pricing.Ref, ChargePolicyRef: policy.Ref,
	}
	result, err := RateCall(CallRatingInput{
		Call: call, Legs: []CallLegUsageRecord{legA, legB},
		MaxCustomerCharge: Money{Nano: 10000, Currency: "USD"},
		CustomerPricing:   pricing, CustomerPolicy: policy,
		ModelPricing: []ModelCustomerPricing{
			{BackendID: "backend", ModelID: "model-a", Pricing: pricing},
			{BackendID: "backend", ModelID: "model-b", Pricing: modelCard},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// model-a uses default card (100 input + 3 fixed); model-b card has no fixed charges (1000 input).
	if got, want := result.CustomerCharge.Nano, int64(1103); got != want {
		t.Fatalf("customer = %d, want %d", got, want)
	}
}
