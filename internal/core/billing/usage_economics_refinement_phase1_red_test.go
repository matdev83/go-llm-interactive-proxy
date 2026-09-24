package billing

import (
	"errors"
	"testing"
)

// TestRefinementRetailFixedFeeIsAppliedOncePerCall_RED records the Phase 1
// retail invariant for a call with multiple accepted B-legs. Fixed commercial
// charges belong to the declared request/call scope; they must not be charged
// once per selected B-leg. The current V1 rate path applies the fee in
// chargeLeg, so this test is intentionally RED until the V2 retail selector
// owns scope-level fixed charges.
func TestRefinementRetailFixedFeeIsAppliedOncePerCall_RED(t *testing.T) {
	t.Parallel()

	result := rateCallFromLegs(t, TurnOutcomeCompleted, []CallLegUsageRecord{
		// Both attempts are accepted for operator COGS and the policy deliberately
		// selects all potential B-legs for the retail calculation.
		testLeg("b-loser", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{}, true),
		testLeg("b-winner", SurfacedYes, 1_000_000, 1_000_000, MoneyEvidence{}, true),
	}, ratingPolicy(ChargeAllPotentialLegs), 1000)

	// 2 x (100 input + 200 output) + one 3-nano request fee.
	const want int64 = 603
	if result.CustomerCharge.Nano != want {
		t.Fatalf("customer charge = %d, want %d (one fixed fee per call)", result.CustomerCharge.Nano, want)
	}
}

// TestRefinementAttemptedMissingProviderEvidenceIsNotReconciledZero_RED keeps
// attempted-but-unmeasured work distinct from a B-leg that never started.
// Returning a reconciled zero for the former would understate operator COGS
// and hides an evidence gap. The current V1 fallback takes that path.
func TestRefinementAttemptedMissingProviderEvidenceIsNotReconciledZero_RED(t *testing.T) {
	t.Parallel()

	callID := mustBillingCallID(t)
	attempted := testCallLegUsageRecord(callID, "b-attempted")
	attempted.Outcome = LegOutcomeFailed
	attempted.Surfaced = SurfacedNo
	attempted.Evidence = FinalBillingEvidence{
		Source:    EvidenceSourceUnavailable,
		Authority: EvidenceAuthorityUnavailable,
	}

	result, err := RateProviderCost(attempted, nil, "USD")
	if err != nil && !errors.Is(err, ErrUnreconciledCost) {
		t.Fatalf("attempted missing evidence returned untyped error %v; want nil unreconciled status or ErrUnreconciledCost", err)
	}
	if result.AmountPresent || result.Reconciled {
		t.Fatalf("attempted missing evidence was reported as reconciled zero: result=%+v err=%v", result, err)
	}
	if result.UnreconciledReason == "" && !errors.Is(err, ErrUnreconciledCost) {
		t.Fatalf("attempted missing evidence lacks an explicit unreconciled reason: result=%+v", result)
	}
}

// TestRefinementRetailSelectionMatrixCharacterization records the existing
// production selector behavior for retry/failover attempts. Retail charging
// selects the surfaced winner for a completed turn, while the explicit
// all-potential policy includes every accepted attempted B-leg. Rejected and
// never-started rows have no accepted provider evidence and are excluded.
// Fixed charges are disabled here so this matrix isolates B-leg selection;
// TestRefinementRetailFixedFeeIsAppliedOncePerCall_RED covers the separate
// call-scoped fee defect.
func TestRefinementRetailSelectionMatrixCharacterization(t *testing.T) {
	t.Parallel()

	makeLeg := func(id string, surfaced SurfacedState, outcome LegOutcome, seq int, accepted bool) CallLegUsageRecord {
		input, output := int64(-1), int64(-1)
		if accepted {
			input, output = 1_000_000, 1_000_000
		}
		leg := testLeg(id, surfaced, input, output, MoneyEvidence{}, accepted)
		leg.Outcome = outcome
		leg.AttemptSeq = seq
		return leg
	}
	base := []CallLegUsageRecord{
		makeLeg("b-retry", SurfacedNo, LegOutcomeFailed, 1, true),
		makeLeg("b-loser", SurfacedNo, LegOutcomeLoser, 2, true),
		makeLeg("b-winner", SurfacedYes, LegOutcomeWinner, 3, true),
		makeLeg("b-rejected", SurfacedNo, LegOutcomeRejected, 4, false),
		makeLeg("b-never-started", SurfacedNo, LegOutcomeNeverStarted, 5, false),
	}

	for _, tc := range []struct {
		name    string
		scope   ChargePolicyScope
		outcome TurnOutcome
		want    int64
	}{
		{name: "surfaced completed winner only", scope: ChargeSurfacedTurn, outcome: TurnOutcomeCompleted, want: 300},
		{name: "all potential includes retry loser winner", scope: ChargeAllPotentialLegs, outcome: TurnOutcomeCompleted, want: 900},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := ratingPolicy(tc.scope)
			policy.IncludeFixedCharges = false
			legs := append([]CallLegUsageRecord(nil), base...)
			result := rateCallFromLegs(t, tc.outcome, legs, policy, 10_000)
			if result.CustomerCharge.Nano != tc.want {
				t.Fatalf("customer charge = %d, want %d for %s", result.CustomerCharge.Nano, tc.want, tc.name)
			}
		})
	}

	t.Run("surfaced interrupted latest accepted attempt", func(t *testing.T) {
		policy := ratingPolicy(ChargeSurfacedTurn)
		policy.IncludeFixedCharges = false
		legs := append([]CallLegUsageRecord(nil), base[:2]...)
		for i := range legs {
			legs[i].Surfaced = SurfacedNo
		}
		legs[1].Evidence.InputTokens.Value = 2_000_000
		result := rateCallFromLegs(t, TurnOutcomeCanceled, legs, policy, 10_000)
		if result.CustomerCharge.Nano != 400 {
			t.Fatalf("interrupted customer charge = %d, want latest accepted B-leg charge 400", result.CustomerCharge.Nano)
		}
	})
}

// TestRefinementProviderCostMatrixCharacterization records the independent
// operator-COGS path for every payable attempted B-leg. The V1 API rates each
// leg independently and has no cost-pass-through selector; the sum assertion
// is therefore only a characterization of the available per-leg production
// contract, not proof of the future retail pass-through policy.
func TestRefinementProviderCostMatrixCharacterization(t *testing.T) {
	t.Parallel()

	callID := mustBillingCallID(t)
	rows := []struct {
		id       string
		outcome  LegOutcome
		surfaced SurfacedState
		amount   int64
	}{
		{id: "b-retry", outcome: LegOutcomeFailed, surfaced: SurfacedNo, amount: 5},
		{id: "b-loser", outcome: LegOutcomeLoser, surfaced: SurfacedNo, amount: 7},
		{id: "b-winner", outcome: LegOutcomeWinner, surfaced: SurfacedYes, amount: 11},
	}
	var total int64
	for _, row := range rows {
		leg := testCallLegUsageRecord(callID, row.id)
		leg.Outcome = row.outcome
		leg.Surfaced = row.surfaced
		leg.Evidence.Cost = MoneyEvidence{NanoUnits: row.amount, Currency: "USD", Present: true}
		leg.Evidence.Source = EvidenceSourceProviderReported
		leg.Evidence.Authority = EvidenceAuthorityAuthoritative
		got, err := RateProviderCost(leg, nil, "USD")
		if err != nil {
			t.Fatalf("RateProviderCost(%s): %v", row.id, err)
		}
		if !got.AmountPresent || !got.Reconciled || !got.Authoritative || got.Amount.Nano != row.amount {
			t.Fatalf("provider cost(%s) = %+v, want authoritative %d", row.id, got, row.amount)
		}
		total += got.Amount.Nano
	}
	if total != 23 {
		t.Fatalf("payable retry/loser/winner COGS total = %d, want 23", total)
	}
}

// TestRefinementNeverStartedEvidenceMayRemainKnownZero is a passing
// characterization: a B-leg explicitly marked never_started has no provider
// exposure and can remain a known zero. It is paired with the RED attempted
// case above so future changes do not collapse the two states.
func TestRefinementNeverStartedEvidenceMayRemainKnownZero(t *testing.T) {
	t.Parallel()

	callID := mustBillingCallID(t)
	neverStarted := testCallLegUsageRecord(callID, "b-never-started")
	neverStarted.Outcome = LegOutcomeNeverStarted
	neverStarted.Surfaced = SurfacedNo
	neverStarted.Evidence = FinalBillingEvidence{
		Source:    EvidenceSourceUnavailable,
		Authority: EvidenceAuthorityUnavailable,
	}

	result, err := RateProviderCost(neverStarted, nil, "USD")
	if err != nil {
		t.Fatalf("never-started provider cost: %v", err)
	}
	if !result.AmountPresent || !result.Reconciled || result.Amount.Nano != 0 {
		t.Fatalf("never-started result = %+v, want reconciled zero", result)
	}
}
