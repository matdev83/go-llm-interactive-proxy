package billing

import (
	"errors"
	"testing"
)

// Phase 1.4: customer leg selection consumes only persisted authoritative
// sequence. Legacy unknown sequence (AttemptSeq == 0) may be auto-rated only
// for provably sequence-independent cases; otherwise the call fails closed.

func TestRateCallFailsClosedWhenSequenceUnknownAndOrderRequired(t *testing.T) {
	t.Parallel()
	policy := ratingPolicy(ChargeSurfacedTurn)
	legs := []CallLegUsageRecord{
		testLeg("b-1", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{}, true),
		testLeg("b-2", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{}, true),
	}
	// Neither leg carries a known attempt sequence (legacy rows).
	legs[0].AttemptSeq = 0
	legs[1].AttemptSeq = 0
	result, err := rateCallFromLegsReturningError(t, TurnOutcomeCanceled, legs, policy, 1000)
	if !errors.Is(err, ErrBillingAttemptSequenceUnknown) {
		t.Fatalf("canceled interrupt with unknown sequence = err %v (result %+v), want ErrBillingAttemptSequenceUnknown", err, result)
	}
}

func TestRateCallSequenceIndependenceAllowsLegacyUnknownLegs(t *testing.T) {
	t.Parallel()
	policy := ratingPolicy(ChargeSurfacedTurn)

	// Completed call with an unambiguous surfaced winner: sequence is
	// irrelevant, so legacy unknown-sequence legs remain auto-rateable.
	completed := []CallLegUsageRecord{
		testLeg("b-1", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{}, true),
		testLeg("b-2", SurfacedYes, 1_000_000, 1_000_000, MoneyEvidence{}, true),
	}
	completed[0].AttemptSeq = 0
	completed[1].AttemptSeq = 0
	result := rateCallFromLegs(t, TurnOutcomeCompleted, completed, policy, 1000)
	if result.CustomerCharge.Nano != 303 {
		t.Fatalf("completed surfaced winner customer = %d, want 303 (sequence-independent)", result.CustomerCharge.Nano)
	}

	// Failed/canceled call with a single accepted leg needs no order either.
	single := []CallLegUsageRecord{
		testLeg("b-1", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{}, true),
	}
	single[0].AttemptSeq = 0
	result = rateCallFromLegs(t, TurnOutcomeCanceled, single, policy, 1000)
	if result.CustomerCharge.Nano != 303 {
		t.Fatalf("canceled single-leg customer = %d, want 303", result.CustomerCharge.Nano)
	}

	// Charge-all policy never needs order to choose a billable leg.
	chargeAll := ratingPolicy(ChargeAllPotentialLegs)
	all := []CallLegUsageRecord{
		testLeg("b-1", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{}, true),
		testLeg("b-2", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{}, true),
	}
	all[0].AttemptSeq = 0
	all[1].AttemptSeq = 0
	result = rateCallFromLegs(t, TurnOutcomeCanceled, all, chargeAll, 1000)
	if result.CustomerCharge.Nano != 606 {
		t.Fatalf("charge-all unknown-sequence customer = %d, want 606", result.CustomerCharge.Nano)
	}
}

func TestRateCallUsesPersistedAttemptSequenceForInterruptedSelection(t *testing.T) {
	t.Parallel()
	policy := ratingPolicy(ChargeSurfacedTurn)

	// Canceled call, no surfaced leg: latest known attempt (AttemptSeq 2)
	// wins regardless of BLegID lexical order. IDs are opaque; lexical order
	// ("b_a" < "b_z") is the reverse of execution order. Attempts carry
	// distinct token dimensions so selection is observable.
	legA := testLeg("b_z7x9p", SurfacedNo, 1_000_000, -1, MoneyEvidence{}, true) // attempt 1: input only -> 103
	legA.AttemptSeq = 1
	legB := testLeg("b_a1b2c", SurfacedNo, -1, 1_000_000, MoneyEvidence{}, true) // attempt 2: output only -> 203
	legB.AttemptSeq = 2
	result := rateCallFromLegs(t, TurnOutcomeCanceled, []CallLegUsageRecord{legA, legB}, policy, 1000)
	if result.CustomerCharge.Nano != 203 {
		t.Fatalf("interrupted selection customer = %d, want 203 from latest attempt (lexical order must be ignored)", result.CustomerCharge.Nano)
	}
}

func rateCallFromLegsReturningError(t *testing.T, outcome TurnOutcome, legs []CallLegUsageRecord, policy ChargePolicy, maxNano int64) (CallRatingResult, error) {
	t.Helper()
	legs = append([]CallLegUsageRecord(nil), legs...)
	call, err := buildRateCall(callIDOfLegs(t), outcome, legs, policy)
	if err != nil {
		t.Fatal(err)
	}
	return RateCall(CallRatingInput{
		Call: call, Legs: legs, MaxCustomerCharge: Money{Nano: maxNano, Currency: "USD"},
		CustomerPricing: ratingPricing(), CustomerPolicy: policy,
	})
}

func callIDOfLegs(t *testing.T) BillingCallID {
	t.Helper()
	callID, err := NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return callID
}

func buildRateCall(callID BillingCallID, outcome TurnOutcome, legs []CallLegUsageRecord, policy ChargePolicy) (CallUsageRecord, error) {
	ids := make([]string, 0, len(legs))
	for i := range legs {
		legs[i].CallID = callID
		legs[i].ALegID = "a-1"
		ids = append(ids, legs[i].BLegID)
	}
	return CallUsageRecord{
		SchemaVersion: CurrentRecordSchemaVersion, CallID: callID, AccountID: "acct-1",
		ALegID: "a-1", ExpectedBLegIDs: ids, StartedAt: testCallUsageRecord(callID).StartedAt,
		FinishedAt: testCallUsageRecord(callID).FinishedAt,
		Outcome:    outcome, CustomerPricingRef: ratingPricing().Ref, ChargePolicyRef: policy.Ref,
	}, nil
}
