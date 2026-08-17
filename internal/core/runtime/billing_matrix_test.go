package runtime

import (
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestB2BUABillingMatrix(t *testing.T) {
	t.Parallel()

	// 1. Setup pricing/policy snapshots
	pricingRef := billing.VersionRef{ID: "prices", Version: "v1"}
	policyRef := billing.VersionRef{ID: "policy", Version: "v1"}

	defaultPricing := billing.PricingSnapshot{
		Ref:                  pricingRef,
		Currency:             "USD",
		InputPerMillionNano:  1000,
		OutputPerMillionNano: 2000,
		InputRatePresent:     true,
		OutputRatePresent:    true,
	}

	policySurfaced := billing.ChargePolicy{
		Ref:                 policyRef,
		PricingRef:          pricingRef,
		Scope:               billing.ChargeSurfacedTurn,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
	}

	modelPricing := []billing.ModelCustomerPricing{
		{
			BackendID: "back-expensive",
			ModelID:   "model-expensive",
			Pricing: billing.PricingSnapshot{
				Ref:                  pricingRef,
				Currency:             "USD",
				InputPerMillionNano:  5000,
				OutputPerMillionNano: 10000,
				InputRatePresent:     true,
				OutputRatePresent:    true,
			},
		},
		{
			BackendID: "back-cheap",
			ModelID:   "model-cheap",
			Pricing: billing.PricingSnapshot{
				Ref:                  pricingRef,
				Currency:             "USD",
				InputPerMillionNano:  10,
				OutputPerMillionNano: 20,
				InputRatePresent:     true,
				OutputRatePresent:    true,
			},
		},
		{
			BackendID: "back-zero",
			ModelID:   "model-zero",
			Pricing: billing.PricingSnapshot{
				Ref:                  pricingRef,
				Currency:             "USD",
				InputPerMillionNano:  0,
				OutputPerMillionNano: 0,
				InputRatePresent:     true,
				OutputRatePresent:    true,
			},
		},
	}

	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}

	// 2. Define our legs matrix
	// We use opaque B-leg IDs whose lexical order is opposite the actual AttemptSeq.
	// This ensures that sorting or selection logic must use AttemptSeq rather than lexical order.
	legs := []billing.CallLegUsageRecord{
		{
			CallID: callID, ALegID: "a-leg", BLegID: "b_f9b2d8e4", AttemptSeq: 1,
			BackendID: "back-expensive", ProviderID: "prov", ModelID: "model-expensive",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(100, 500000000).UTC(),
			Outcome: billing.LegOutcomeNeverStarted, Surfaced: billing.SurfacedNo,
			Evidence: billing.FinalBillingEvidence{
				InputTokens: billing.Quantity{Value: 1_000_000, Present: true}, OutputTokens: billing.Quantity{Value: 0, Present: true},
			},
		},
		{
			CallID: callID, ALegID: "a-leg", BLegID: "b_d8c4a1b0", AttemptSeq: 2,
			BackendID: "back-expensive", ProviderID: "prov", ModelID: "model-expensive",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(100, 500000000).UTC(),
			Outcome: billing.LegOutcomeFailed, Surfaced: billing.SurfacedNo,
			Evidence: billing.FinalBillingEvidence{
				InputTokens: billing.Quantity{Value: 1_000_000, Present: true}, OutputTokens: billing.Quantity{Value: 0, Present: true},
			},
		},
		{
			CallID: callID, ALegID: "a-leg", BLegID: "b_b3e5f2c1", AttemptSeq: 3,
			BackendID: "back-expensive", ProviderID: "prov", ModelID: "model-expensive",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(100, 500000000).UTC(),
			Outcome: billing.LegOutcomeSwallowed, Surfaced: billing.SurfacedNo,
			Evidence: billing.FinalBillingEvidence{
				InputTokens: billing.Quantity{Value: 1_000_000, Present: true}, OutputTokens: billing.Quantity{Value: 0, Present: true},
			},
		},
		{
			CallID: callID, ALegID: "a-leg", BLegID: "b_9a2f1e8d", AttemptSeq: 4,
			BackendID: "back-cheap", ProviderID: "prov", ModelID: "model-cheap",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(100, 500000000).UTC(),
			Outcome: billing.LegOutcomeLoser, Surfaced: billing.SurfacedNo,
			Evidence: billing.FinalBillingEvidence{
				InputTokens: billing.Quantity{Value: 1_000_000, Present: true}, OutputTokens: billing.Quantity{Value: 0, Present: true},
			},
		},
		{
			CallID: callID, ALegID: "a-leg", BLegID: "b_7d6c5b4a", AttemptSeq: 5,
			BackendID: "back-cheap", ProviderID: "prov", ModelID: "model-cheap",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(100, 500000000).UTC(),
			Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedNo,
			Evidence: billing.FinalBillingEvidence{
				InputTokens: billing.Quantity{Value: 1_000_000, Present: true}, OutputTokens: billing.Quantity{Value: 0, Present: true},
			},
		},
		{
			CallID: callID, ALegID: "a-leg", BLegID: "b_5c4b3a2e", AttemptSeq: 6,
			BackendID: "back-cheap", ProviderID: "prov", ModelID: "model-cheap",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(100, 500000000).UTC(),
			Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
			Evidence: billing.FinalBillingEvidence{
				InputTokens: billing.Quantity{Value: 1_000_000, Present: true}, OutputTokens: billing.Quantity{Value: 0, Present: true},
			},
		},
		{
			CallID: callID, ALegID: "a-leg", BLegID: "b_2a1f0e9d", AttemptSeq: 7,
			BackendID: "back-zero", ProviderID: "prov", ModelID: "model-zero",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(100, 500000000).UTC(),
			Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedNo,
			Evidence: billing.FinalBillingEvidence{
				InputTokens: billing.Quantity{Value: 1_000_000, Present: true}, OutputTokens: billing.Quantity{Value: 0, Present: true},
			},
		},
	}

	expectedIDs := make([]string, 0, len(legs))
	for _, l := range legs {
		expectedIDs = append(expectedIDs, l.BLegID)
	}

	closure := billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             callID,
		AccountID:          "acct",
		ALegID:             "a-leg",
		SessionID:          "sess",
		StartedAt:          time.Unix(100, 0).UTC(),
		FinishedAt:         time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: pricingRef,
		ChargePolicyRef:    policyRef,
		ExpectedBLegIDs:    expectedIDs,
	}

	sealedClosure, err := closure.Seal()
	if err != nil {
		t.Fatal(err)
	}

	sealedLegs := make([]billing.CallLegUsageRecord, 0, len(legs))
	for _, l := range legs {
		sl, err := l.Seal()
		if err != nil {
			t.Fatal(err)
		}
		sealedLegs = append(sealedLegs, sl)
	}

	// 3. Test JoinCompleteCall is independent of append/join order
	complete1, err := billing.JoinCompleteCall(sealedClosure, sealedLegs)
	if err != nil {
		t.Fatalf("JoinCompleteCall basic: %v", err)
	}

	// Shuffle sealedLegs
	r := rand.New(rand.NewSource(42))
	shuffledLegs := append([]billing.CallLegUsageRecord(nil), sealedLegs...)
	r.Shuffle(len(shuffledLegs), func(i, j int) {
		shuffledLegs[i], shuffledLegs[j] = shuffledLegs[j], shuffledLegs[i]
	})

	complete2, err := billing.JoinCompleteCall(sealedClosure, shuffledLegs)
	if err != nil {
		t.Fatalf("JoinCompleteCall shuffled: %v", err)
	}

	// Proving append/join order is irrelevant
	if !reflect.DeepEqual(complete1.Legs, complete2.Legs) {
		t.Fatalf("JoinCompleteCall results differ on legs order:\n1: %+v\n2: %+v", complete1.Legs, complete2.Legs)
	}

	// 4. Rate complete call using ChargeSurfacedTurn policy:
	// Only surfaced winner legs (Seq 6, "b_5c4b3a2e") should be charged.
	// Model price for back-cheap/model-cheap is 10/million.
	// 1,000,000 input tokens * 10/million = 10 nano units.
	ratingInput := billing.CallRatingInput{
		Call:              sealedClosure,
		Legs:              sealedLegs,
		MaxCustomerCharge: billing.Money{Nano: 100000, Currency: "USD"},
		CustomerPricing:   defaultPricing,
		CustomerPolicy:    policySurfaced,
		ModelPricing:      modelPricing,
	}
	result, err := billing.RateCall(ratingInput)
	if err != nil {
		t.Fatalf("RateCall surfaced policy failed: %v", err)
	}

	if result.CustomerCharge.Nano != 10 {
		t.Fatalf("expected CustomerCharge to be 10, got %d", result.CustomerCharge.Nano)
	}

	// 5. Rate using ChargeAllPotentialLegs policy:
	// All potential billable legs should be sum-charged.
	// Billable legs are all accepted legs (winner/loser/failed/canceled/never-started with evidence, etc.):
	// Let's see:
	// - Seq 1: LegOutcomeNeverStarted (cost 0, but input tokens 1,000,000 present, backend back-expensive -> 5000)
	// - Seq 2: LegOutcomeFailed (input tokens 1,000,000 present, backend back-expensive -> 5000)
	// - Seq 3: LegOutcomeSwallowed (input tokens 1,000,000 present, backend back-expensive -> 5000)
	// - Seq 4: LegOutcomeLoser (input tokens 1,000,000 present, backend back-cheap -> 10)
	// - Seq 5: LegOutcomeWinner (input tokens 1,000,000 present, backend back-cheap -> 10)
	// - Seq 6: LegOutcomeWinner (input tokens 1,000,000 present, backend back-cheap -> 10)
	// - Seq 7: LegOutcomeWinner (input tokens 1,000,000 present, backend back-zero -> 0)
	// Total expected charge = 5000 + 5000 + 5000 + 10 + 10 + 10 + 0 = 15030 nano units.
	policyChargeAll := billing.ChargePolicy{
		Ref:                 policyRef,
		PricingRef:          pricingRef,
		Scope:               billing.ChargeAllPotentialLegs,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
	}
	ratingInputChargeAll := billing.CallRatingInput{
		Call:              sealedClosure,
		Legs:              sealedLegs,
		MaxCustomerCharge: billing.Money{Nano: 100000, Currency: "USD"},
		CustomerPricing:   defaultPricing,
		CustomerPolicy:    policyChargeAll,
		ModelPricing:      modelPricing,
	}
	resultChargeAll, err := billing.RateCall(ratingInputChargeAll)
	if err != nil {
		t.Fatalf("RateCall charge-all policy failed: %v", err)
	}

	if resultChargeAll.CustomerCharge.Nano != 15030 {
		t.Fatalf("expected CustomerCharge for charge-all to be 15030, got %d", resultChargeAll.CustomerCharge.Nano)
	}
}
