package billing

import (
	"errors"
	"testing"
	"time"
)

// phase1LegacyCustomerCharge is a test-only characterization oracle for the
// predecessor TUR/LUR bridge. It deliberately operates on current records so
// the production bridge can be removed without keeping legacy domain types in
// the test binary. The expected selection and arithmetic are frozen here before
// the native implementation is cut over.
func phase1LegacyCustomerCharge(legs []CallLegUsageRecord, outcome TurnOutcome, policy ChargePolicy, pricing PricingSnapshot) (int64, error) {
	accepted := make([]CallLegUsageRecord, 0, len(legs))
	for _, leg := range legs {
		if providerAcceptedEvidence(leg.Evidence) {
			accepted = append(accepted, leg)
		}
	}
	selected := accepted
	if policy.Scope != ChargeAllPotentialLegs {
		if outcome == TurnOutcomeCompleted {
			selected = selected[:0]
			for _, leg := range accepted {
				if leg.Surfaced == SurfacedYes {
					selected = append(selected, leg)
				}
			}
		} else {
			surfaced := make([]CallLegUsageRecord, 0, 1)
			for _, leg := range accepted {
				if leg.Surfaced == SurfacedYes {
					surfaced = append(surfaced, leg)
				}
			}
			if len(surfaced) > 0 {
				selected = surfaced
			} else if len(accepted) > 1 {
				best := accepted[0]
				for _, leg := range accepted {
					if leg.AttemptSeq <= 0 {
						return 0, ErrBillingAttemptSequenceUnknown
					}
					if leg.AttemptSeq > best.AttemptSeq {
						best = leg
					}
				}
				selected = []CallLegUsageRecord{best}
			}
		}
	}
	var total int64
	for _, leg := range selected {
		strict := outcome == TurnOutcomeCompleted && leg.Surfaced == SurfacedYes
		amount, err := chargeLegCurrentForTest(leg, pricing, policy, strict)
		if err != nil {
			return 0, err
		}
		if total > int64(^uint64(0)>>1)-amount {
			return 0, ErrRatingInvalid
		}
		total += amount
	}
	return total, nil
}

func chargeLegCurrentForTest(leg CallLegUsageRecord, pricing PricingSnapshot, policy ChargePolicy, strict bool) (int64, error) {
	var total int64
	add := func(v int64) error {
		if v < 0 || total > int64(^uint64(0)>>1)-v {
			return ErrRatingInvalid
		}
		total += v
		return nil
	}
	if policy.IncludeInputTokens {
		if !leg.Evidence.InputTokens.Present {
			if strict {
				return 0, ErrRatingEvidenceMissing
			}
		} else if !pricing.InputRatePresent {
			return 0, ErrRatingEvidenceMissing
		} else if v, err := exactTokensAtRate(leg.Evidence.InputTokens.Value, pricing.InputPerMillionNano); err != nil {
			return 0, err
		} else if err := add(v); err != nil {
			return 0, err
		}
	}
	if policy.IncludeOutputTokens {
		if !leg.Evidence.OutputTokens.Present {
			if strict {
				return 0, ErrRatingEvidenceMissing
			}
		} else if !pricing.OutputRatePresent {
			return 0, ErrRatingEvidenceMissing
		} else if v, err := exactTokensAtRate(leg.Evidence.OutputTokens.Value, pricing.OutputPerMillionNano); err != nil {
			return 0, err
		} else if err := add(v); err != nil {
			return 0, err
		}
	}
	if policy.IncludeFixedCharges {
		for _, component := range pricing.FixedCharges {
			v, err := componentAmount(component, pricing.Currency)
			if err != nil {
				return 0, err
			}
			if err := add(v); err != nil {
				return 0, err
			}
		}
	}
	if policy.IncludeResourceCharges {
		for _, component := range pricing.ResourceCharges {
			v, err := componentAmount(component, pricing.Currency)
			if err != nil {
				return 0, err
			}
			if err := add(v); err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}

func phase1UnacceptedLeg(id string, outcome LegOutcome) CallLegUsageRecord {
	leg := testLeg(id, SurfacedNo, -1, -1, MoneyEvidence{}, false)
	leg.Outcome = outcome
	leg.Evidence.Source = EvidenceSourceUnavailable
	leg.Evidence.Authority = EvidenceAuthorityUnavailable
	return leg
}

func TestPhase1NativeRatingDifferentialAgainstLegacyScenarios(t *testing.T) {
	t.Parallel()
	pricing := ratingPricing()
	cases := []struct {
		name    string
		outcome TurnOutcome
		policy  ChargePolicy
		legs    []CallLegUsageRecord
		want    int64
		wantErr error
	}{
		{
			name:    "surfaced completed",
			outcome: TurnOutcomeCompleted, policy: ratingPolicy(ChargeSurfacedTurn),
			legs: []CallLegUsageRecord{
				testLeg("b-failed", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{}, true),
				testLeg("b-winner", SurfacedYes, 1_000_000, 1_000_000, MoneyEvidence{}, true),
			}, want: 303,
		},
		{
			name:    "failover",
			outcome: TurnOutcomeCanceled, policy: ratingPolicy(ChargeSurfacedTurn),
			legs: []CallLegUsageRecord{
				testLeg("b-first", SurfacedNo, 1_000_000, -1, MoneyEvidence{}, true),
				testLeg("b-second", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{}, true),
			}, want: 303,
		},
		{
			name:    "parallel all potential evidence first",
			outcome: TurnOutcomeCompleted, policy: ratingPolicy(ChargeAllPotentialLegs),
			legs: []CallLegUsageRecord{
				testLeg("b-accepted-a", SurfacedYes, 1_000_000, 1_000_000, MoneyEvidence{}, true),
				testLeg("b-accepted-b", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{}, true),
				phase1UnacceptedLeg("b-never-started", LegOutcomeNeverStarted),
				phase1UnacceptedLeg("b-rejected", LegOutcomeRejected),
				phase1UnacceptedLeg("b-no-evidence", LegOutcomeFailed),
			}, want: 606,
		},
		{
			name:    "interrupted latest accepted sequence",
			outcome: TurnOutcomeCanceled, policy: ratingPolicy(ChargeSurfacedTurn),
			legs: []CallLegUsageRecord{
				testLeg("b-attempt-1", SurfacedNo, 1_000_000, -1, MoneyEvidence{}, true),
				testLeg("b-attempt-2", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{}, true),
			}, want: 303,
		},
		{
			name:    "sequence unknown but surfaced is unambiguous",
			outcome: TurnOutcomeCompleted, policy: ratingPolicy(ChargeSurfacedTurn),
			legs: []CallLegUsageRecord{testLeg("b-surfaced", SurfacedYes, 1_000_000, 1_000_000, MoneyEvidence{}, true)}, want: 303,
		},
		{
			name:    "sequence unknown interrupted fails closed",
			outcome: TurnOutcomeCanceled, policy: ratingPolicy(ChargeSurfacedTurn),
			legs: []CallLegUsageRecord{
				testLeg("b-unknown-a", SurfacedNo, 1_000_000, -1, MoneyEvidence{}, true),
				testLeg("b-unknown-b", SurfacedNo, 1_000_000, 1_000_000, MoneyEvidence{}, true),
			}, wantErr: ErrBillingAttemptSequenceUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			legs := append([]CallLegUsageRecord(nil), tc.legs...)
			for i := range legs {
				legs[i].AttemptSeq = i + 1
				if tc.name == "sequence unknown but surfaced is unambiguous" || tc.name == "sequence unknown interrupted fails closed" {
					legs[i].AttemptSeq = 0
				}
			}
			want, legacyErr := phase1LegacyCustomerCharge(legs, tc.outcome, tc.policy, pricing)
			if tc.wantErr != nil {
				if !errors.Is(legacyErr, tc.wantErr) {
					t.Fatalf("legacy oracle error = %v, want %v", legacyErr, tc.wantErr)
				}
			} else if legacyErr != nil || want != tc.want {
				t.Fatalf("legacy oracle = %d/%v, want %d/nil", want, legacyErr, tc.want)
			}

			callID := mustBillingCallID(t)
			for i := range legs {
				legs[i].CallID = callID
				legs[i].ALegID = "a-1"
			}
			ids := make([]string, 0, len(legs))
			for _, leg := range legs {
				ids = append(ids, leg.BLegID)
			}
			call := CallUsageRecord{SchemaVersion: CurrentRecordSchemaVersion, CallID: callID, AccountID: "acct-1", ALegID: "a-1", ExpectedBLegIDs: ids, StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(), Outcome: tc.outcome, CustomerPricingRef: pricing.Ref, ChargePolicyRef: tc.policy.Ref}
			got, err := RateCall(CallRatingInput{Call: call, Legs: legs, MaxCustomerCharge: Money{Nano: 10000, Currency: "USD"}, CustomerPricing: pricing, CustomerPolicy: tc.policy})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("native error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("native rating: %v", err)
			}
			if got.CustomerCharge.Nano != want {
				t.Fatalf("native charge = %d, legacy charge = %d", got.CustomerCharge.Nano, want)
			}
		})
	}
}
