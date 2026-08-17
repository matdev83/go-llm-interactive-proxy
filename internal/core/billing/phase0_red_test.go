package billing

import (
	"testing"
	"time"
)

func TestRateCall_ReversedLexicalBLegIDSequence(t *testing.T) {
	// Task 0.1: Add RED regression for reversed lexical B-leg IDs versus real attempt sequence.
	// Cover failed/canceled/no-surfaced selection and prove current positional reconstruction
	// selects the wrong leg. IDs must be opaque and lexical order reversed relative to execution.
	t.Parallel()

	callID := mustBillingCallID(t)
	policy := ratingPolicy(ChargeSurfacedTurn)
	pricing := ratingPricing()

	// Leg A: execution attempt 1, but lexically later ("b_z7x9p"). Has 1,000,000 input tokens.
	legA := testCallLegUsageRecord(callID, "b_z7x9p")
	legA.AttemptSeq = 1
	legA.Surfaced = SurfacedNo
	legA.Outcome = LegOutcomeFailed
	legA.Evidence = FinalBillingEvidence{
		InputTokens: Quantity{Value: 1_000_000, Present: true},
		Source:      EvidenceSourceProviderReported,
		Authority:   EvidenceAuthorityAuthoritative,
	}

	// Leg B: execution attempt 2, but lexically earlier ("b_a1b2c"). Has 2,000,000 input tokens.
	legB := testCallLegUsageRecord(callID, "b_a1b2c")
	legB.AttemptSeq = 2
	legB.Surfaced = SurfacedNo
	legB.Outcome = LegOutcomeFailed
	legB.Evidence = FinalBillingEvidence{
		InputTokens: Quantity{Value: 2_000_000, Present: true},
		Source:      EvidenceSourceProviderReported,
		Authority:   EvidenceAuthorityAuthoritative,
	}

	// Note: We pass them to RateCall in lexical/sorted order of their BLegID.
	// ExpectedBLegIDs in CallUsageRecord is also sorted.
	call := CallUsageRecord{
		SchemaVersion:      CurrentRecordSchemaVersion,
		CallID:             callID,
		AccountID:          "acct-1",
		ALegID:             "a-shared",
		SessionID:          "sess-shared",
		StartedAt:          time.Unix(100, 0).UTC(),
		FinishedAt:         time.Unix(101, 0).UTC(),
		Outcome:            TurnOutcomeFailed, // canceled/failed call
		CustomerPricingRef: pricing.Ref,
		ChargePolicyRef:    policy.Ref,
		ExpectedBLegIDs:    []string{"b_a1b2c", "b_z7x9p"},
	}

	// Current positional reconstruction uses index in the Legs slice to assign Seq.
	// If Legs is passed sorted by BLegID: [legB ("b_a1b2c"), legA ("b_z7x9p")],
	// legB gets Seq = 1 and legA gets Seq = 2.
	// Thus, it will select legA (Seq = 2) and rate it (charging for 100 nano).
	// Desired behavior: Select the latest attempt (AttemptSeq = 2), which is legB ("b_a1b2c"), charging 200 nano.
	legs := []CallLegUsageRecord{legB, legA}

	result, err := RateCall(CallRatingInput{
		Call:              call,
		Legs:              legs,
		MaxCustomerCharge: Money{Nano: 1000, Currency: "USD"},
		CustomerPricing:   pricing,
		CustomerPolicy:    policy,
	})
	if err != nil {
		t.Fatalf("RateCall: %v", err)
	}

	// Calculate expected charge based on legB (2,000,000 tokens * 100 per million nano = 200 nano,
	// plus 3 nano fixed charge = 203 nano).
	// Under current positional reconstruction, it selects legA (1,000,000 tokens * 100 per million nano = 100 nano
	// plus 3 nano fixed charge = 103 nano).
	// We assert the desired behavior (203 nano).
	if got, want := result.CustomerCharge.Nano, int64(203); got != want {
		t.Errorf("CustomerCharge = %d, want %d (RED regression: current positional reconstruction selects legA instead of legB)", got, want)
	}
}
