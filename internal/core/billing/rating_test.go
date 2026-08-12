package billing

import (
	"context"
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

func ratingAuthorization(amount int64) Authorization {
	return Authorization{
		ID: "auth-1", AccountID: "acct-1", TURKey: "acct-1:turn-1",
		Amount: Money{Nano: amount, Currency: "USD"}, PricingRef: ratingPricing().Ref,
		ChargePolicyRef: ratingPolicy(ChargeSurfacedTurn).Ref,
	}
}

func ratingRecord(outcome TurnOutcome, surfaced []SurfacedState, costs []MoneyEvidence) TurnUsageRecord {
	legs := make([]LegUsageRecord, len(surfaced))
	for i := range surfaced {
		seq := i + 1
		ev := FinalBillingEvidence{
			InputTokens:  Quantity{Value: 1_000_000, Present: true},
			OutputTokens: Quantity{Value: 1_000_000, Present: true},
			Cost:         costs[i],
		}
		// Present monetary evidence in fixtures is authoritative unless a test
		// overrides Authority; estimated/unknown presence must not be treated as COGS.
		if costs[i].Present {
			ev.Authority = EvidenceAuthorityAuthoritative
			ev.Source = EvidenceSourceProviderReported
		}
		legs[i] = LegUsageRecord{
			ALegID: "a-1", BLegID: "b-" + string(rune('1'+i)), Seq: seq,
			BackendID: "backend", ProviderID: "provider", ModelID: "model",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
			Outcome: LegOutcomeWinner, Surfaced: surfaced[i],
			Evidence:        ev,
			OperatorRateRef: VersionRef{ID: "operator-rates", Version: "v1"},
		}
	}
	return TurnUsageRecord{
		SchemaVersion: CurrentRecordSchemaVersion, AccountID: "acct-1", TurnID: "turn-1",
		ALegID: "a-1", AuthorizationID: "auth-1", StartedAt: time.Unix(100, 0).UTC(),
		FinishedAt: time.Unix(101, 0).UTC(), Outcome: outcome,
		CustomerPricingRef: ratingPricing().Ref, ChargePolicyRef: ratingPolicy(ChargeSurfacedTurn).Ref,
		Legs: legs,
	}
}

func operatorRate() OperatorRateSnapshot {
	return OperatorRateSnapshot{
		Ref: VersionRef{ID: "operator-rates", Version: "v1"}, Currency: "USD",
		InputPerMillionNano: 50, OutputPerMillionNano: 75,
		InputRatePresent: true, OutputRatePresent: true,
	}
}

func TestRateTurnChargesSurfacedTurnAndKeepsProviderCostSeparate(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedNo, SurfacedYes}, []MoneyEvidence{
		{NanoUnits: 10, Currency: "USD", Present: true},
		{NanoUnits: 0, Currency: "USD", Present: true},
	})
	result, err := RateTurn(RatingInput{
		Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(),
		CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{operatorRate()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CustomerCharge != (Money{Nano: 303, Currency: "USD"}) {
		t.Fatalf("customer charge = %+v, want 303 USD", result.CustomerCharge)
	}
	if len(result.OperatorCosts) != 2 || !result.OperatorCosts[0].AmountPresent || !result.OperatorCosts[1].AmountPresent || result.OperatorCosts[0].Amount.Nano != 10 || result.OperatorCosts[1].Amount.Nano != 0 {
		t.Fatalf("operator costs = %+v", result.OperatorCosts)
	}
	if result.UnreconciledCost {
		t.Fatal("authoritative costs must be reconciled")
	}
}

func TestRateTurnBillsObservedUsageOnFailureAndCancel(t *testing.T) {
	t.Parallel()
	// OpenRouter-style: cancel/fail after provider acceptance still bills observed
	// input+output (and fixed components). Authorization must cover the actual.
	for _, outcome := range []TurnOutcome{TurnOutcomeFailed, TurnOutcomeCanceled} {
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			record := ratingRecord(outcome, []SurfacedState{SurfacedYes}, []MoneyEvidence{{NanoUnits: 10, Currency: "USD", Present: true}})
			result, err := RateTurn(RatingInput{Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(), CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{operatorRate()}})
			if err != nil {
				t.Fatal(err)
			}
			if result.CustomerCharge.Nano != 303 {
				t.Fatalf("customer charge = %+v, want 303 for observed usage", result.CustomerCharge)
			}
		})
	}
}

func TestRateTurnCancelWithUnsurfacedOutputStillBillsProviderAcceptedUsage(t *testing.T) {
	t.Parallel()
	// Connectivity cancel often leaves SurfacedNo even though completion tokens exist.
	record := ratingRecord(TurnOutcomeCanceled, []SurfacedState{SurfacedNo}, []MoneyEvidence{{NanoUnits: 4, Currency: "USD", Present: true}})
	result, err := RateTurn(RatingInput{Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(), CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{operatorRate()}})
	if err != nil {
		t.Fatal(err)
	}
	if result.CustomerCharge.Nano != 303 {
		t.Fatalf("cancel customer charge = %+v, want 303", result.CustomerCharge)
	}
}

func TestRateTurnSurfacedTurnInterruptBillsOneLogicalAcceptedLeg(t *testing.T) {
	t.Parallel()
	// ChargeSurfacedTurn admission holds max(routes), not the sum of failover
	// alternatives. Interrupt rating must bill one logical turn (latest accepted
	// Seq) so actual charge cannot exceed that hold (Req 4.6 / 12.9 / 12.14).
	const oneLeg = int64(303)
	for _, outcome := range []TurnOutcome{TurnOutcomeCanceled, TurnOutcomeFailed} {
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			record := ratingRecord(outcome, []SurfacedState{SurfacedNo, SurfacedNo}, []MoneyEvidence{
				{NanoUnits: 10, Currency: "USD", Present: true},
				{NanoUnits: 4, Currency: "USD", Present: true},
			})
			auth := ratingAuthorization(oneLeg)
			result, err := RateTurn(RatingInput{
				Record: record, Authorization: auth, CustomerPricing: ratingPricing(),
				CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{operatorRate()},
			})
			if err != nil {
				t.Fatalf("surfaced-turn interrupt must fit the max-route hold: %v", err)
			}
			if result.CustomerCharge.Nano != oneLeg {
				t.Fatalf("customer charge = %+v, want one logical turn %d (latest Seq), not the failover sum", result.CustomerCharge, oneLeg)
			}
		})
	}
}

func TestRateTurnAllPotentialLegsInterruptStillSumsAcceptedLegs(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeCanceled, []SurfacedState{SurfacedNo, SurfacedNo}, []MoneyEvidence{
		{NanoUnits: 10, Currency: "USD", Present: true},
		{NanoUnits: 4, Currency: "USD", Present: true},
	})
	policy := ratingPolicy(ChargeAllPotentialLegs)
	record.ChargePolicyRef = policy.Ref
	auth := Authorization{
		ID: "auth-1", AccountID: "acct-1", TURKey: "acct-1:turn-1",
		Amount: Money{Nano: 610, Currency: "USD"}, PricingRef: ratingPricing().Ref, ChargePolicyRef: policy.Ref,
	}
	result, err := RateTurn(RatingInput{
		Record: record, Authorization: auth, CustomerPricing: ratingPricing(),
		CustomerPolicy: policy, OperatorRates: []OperatorRateSnapshot{operatorRate()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CustomerCharge.Nano != 606 {
		t.Fatalf("all-potential interrupt charge = %+v, want 606 (sum of accepted legs)", result.CustomerCharge)
	}
}

func TestRateTurnRejectedProviderAttemptIsZeroCustomerCharge(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeFailed, []SurfacedState{SurfacedNo}, []MoneyEvidence{{Currency: "USD", Present: false}})
	record.Legs[0].Evidence.InputTokens = Quantity{}
	record.Legs[0].Evidence.OutputTokens = Quantity{}
	record.Legs[0].Outcome = LegOutcomeFailed
	policy := ratingPolicy(ChargeSurfacedTurn)
	policy.IncludeFixedCharges = false
	record.ChargePolicyRef = policy.Ref
	auth := ratingAuthorization(1000)
	auth.ChargePolicyRef = policy.Ref
	result, err := RateTurn(RatingInput{Record: record, Authorization: auth, CustomerPricing: ratingPricing(), CustomerPolicy: policy, OperatorRates: []OperatorRateSnapshot{operatorRate()}})
	if err != nil {
		t.Fatal(err)
	}
	if result.CustomerCharge.Nano != 0 {
		t.Fatalf("rejected attempt customer charge = %+v, want 0", result.CustomerCharge)
	}
	if result.UnreconciledCost {
		t.Fatal("rejected never-started leg must not block TUR as unreconciled_cost")
	}
	if len(result.OperatorCosts) != 1 || !result.OperatorCosts[0].Reconciled || !result.OperatorCosts[0].AmountPresent || result.OperatorCosts[0].Amount.Nano != 0 {
		t.Fatalf("rejected operator cost = %+v, want reconciled 0", result.OperatorCosts)
	}
}

func TestRateTurnInterruptedTurnFailsClosedWhenIncludedRateMissing(t *testing.T) {
	t.Parallel()
	// Req 12.13 skips missing quantities on interrupt, not missing bound rates.
	for _, outcome := range []TurnOutcome{TurnOutcomeCanceled, TurnOutcomeFailed, TurnOutcomeUnknown} {
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			record := ratingRecord(outcome, []SurfacedState{SurfacedNo}, []MoneyEvidence{{Currency: "USD", Present: false}})
			pricing := ratingPricing()
			pricing.InputRatePresent = false
			pricing.FixedCharges = nil
			policy := ratingPolicy(ChargeSurfacedTurn)
			policy.IncludeFixedCharges = false
			record.ChargePolicyRef = policy.Ref
			auth := ratingAuthorization(1000)
			auth.ChargePolicyRef = policy.Ref
			_, err := RateTurn(RatingInput{
				Record: record, Authorization: auth, CustomerPricing: pricing, CustomerPolicy: policy,
				OperatorRates: []OperatorRateSnapshot{operatorRate()},
			})
			if !errors.Is(err, ErrRatingEvidenceMissing) {
				t.Fatalf("err = %v, want ErrRatingEvidenceMissing", err)
			}
		})
	}
}

func TestRateTurnRejectedFailoverLegDoesNotUnreconcileWinningTurn(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedNo, SurfacedYes}, []MoneyEvidence{
		{Currency: "USD", Present: false}, {NanoUnits: 0, Currency: "USD", Present: true},
	})
	record.Legs[0].Outcome = LegOutcomeFailed
	record.Legs[0].Evidence.InputTokens = Quantity{}
	record.Legs[0].Evidence.OutputTokens = Quantity{}
	result, err := RateTurn(RatingInput{
		Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(),
		CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{operatorRate()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CustomerCharge.Nano != 303 || result.UnreconciledCost {
		t.Fatalf("winning-turn rating = %+v, want customer 303 and reconciled", result)
	}
	if len(result.OperatorCosts) != 2 || !result.OperatorCosts[0].Reconciled || result.OperatorCosts[0].Amount.Nano != 0 || !result.OperatorCosts[1].Authoritative {
		t.Fatalf("operator costs = %+v, want rejected $0 plus authoritative winner", result.OperatorCosts)
	}
}

func TestRateTurnCancelBillsInputOnlyWhenOutputAbsent(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeCanceled, []SurfacedState{SurfacedNo}, []MoneyEvidence{{Currency: "USD", Present: false}})
	record.Legs[0].Evidence.OutputTokens = Quantity{}
	policy := ratingPolicy(ChargeSurfacedTurn)
	policy.IncludeFixedCharges = false
	policy.IncludeOutputTokens = true
	record.ChargePolicyRef = policy.Ref
	auth := ratingAuthorization(1000)
	auth.ChargePolicyRef = policy.Ref
	result, err := RateTurn(RatingInput{Record: record, Authorization: auth, CustomerPricing: ratingPricing(), CustomerPolicy: policy, OperatorRates: []OperatorRateSnapshot{operatorRate()}})
	if err != nil {
		t.Fatal(err)
	}
	// 1M input * 100 nano/M = 100; output absent is skipped (not an error) on cancel.
	if result.CustomerCharge.Nano != 100 {
		t.Fatalf("input-only cancel charge = %+v, want 100", result.CustomerCharge)
	}
}

func TestRateTurnAllPotentialLegsIncludeFailedProviderCostAndCustomerObservedUsage(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedNo, SurfacedYes}, []MoneyEvidence{
		{Currency: "USD", Present: false}, {NanoUnits: 0, Currency: "USD", Present: true},
	})
	record.Legs[0].Outcome = LegOutcomeFailed
	record.Legs[0].OperatorRateRef = VersionRef{ID: "operator-rates", Version: "v1"}
	record.Legs[1].OperatorRateRef = VersionRef{ID: "operator-rates", Version: "v2"}
	rateV2 := operatorRate()
	rateV2.Ref.Version = "v2"
	rateV2.InputPerMillionNano = 60
	policy := ratingPolicy(ChargeSurfacedTurn)
	result, err := RateTurn(RatingInput{Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(), CustomerPolicy: policy, OperatorRates: []OperatorRateSnapshot{operatorRate(), rateV2}})
	if err != nil {
		t.Fatal(err)
	}
	if result.CustomerCharge.Nano != 303 || result.UnreconciledCost || len(result.OperatorCosts) != 2 || !result.OperatorCosts[0].AmountPresent || !result.OperatorCosts[1].AmountPresent || result.OperatorCosts[0].Amount.Nano != 125 || result.OperatorCosts[1].Amount.Nano != 0 {
		t.Fatalf("failed-leg rating = %+v", result)
	}

	record.Outcome = TurnOutcomeFailed
	result, err = RateTurn(RatingInput{Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(), CustomerPolicy: policy, OperatorRates: []OperatorRateSnapshot{operatorRate(), rateV2}})
	if err != nil {
		t.Fatal(err)
	}
	// ChargeSurfacedTurn interrupt bills one logical turn (surfaced accepted leg).
	if result.CustomerCharge.Nano != 303 || len(result.OperatorCosts) != 2 || result.UnreconciledCost {
		t.Fatalf("failed-turn rating = %+v", result)
	}
}

func TestRateTurnAllPotentialLegsAndBound(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedNo, SurfacedYes}, []MoneyEvidence{
		{NanoUnits: 0, Currency: "USD", Present: true}, {NanoUnits: 0, Currency: "USD", Present: true},
	})
	policy := ratingPolicy(ChargeAllPotentialLegs)
	record.ChargePolicyRef = policy.Ref
	result, err := RateTurn(RatingInput{Record: record, Authorization: Authorization{ID: "auth-1", AccountID: "acct-1", TURKey: record.AccountID + ":" + record.TurnID, Amount: Money{Nano: 610, Currency: "USD"}, PricingRef: ratingPricing().Ref, ChargePolicyRef: policy.Ref}, CustomerPricing: ratingPricing(), CustomerPolicy: policy, OperatorRates: []OperatorRateSnapshot{operatorRate()}})
	if err != nil {
		t.Fatal(err)
	}
	if result.CustomerCharge.Nano != 606 {
		t.Fatalf("all-leg customer charge = %+v, want 606 USD", result.CustomerCharge)
	}
	if _, err := RateTurn(RatingInput{Record: record, Authorization: ratingAuthorization(1), CustomerPricing: ratingPricing(), CustomerPolicy: policy, OperatorRates: []OperatorRateSnapshot{operatorRate()}}); !errors.Is(err, ErrActualChargeExceedsAuthorization) {
		t.Fatalf("overage error = %v, want ErrActualChargeExceedsAuthorization", err)
	}
}

func TestRateTurnCompletedAllPotentialLegsRejectedSiblingIsZeroNotHardFail(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedNo, SurfacedYes}, []MoneyEvidence{
		{Currency: "USD", Present: false}, {NanoUnits: 0, Currency: "USD", Present: true},
	})
	record.Legs[0].Outcome = LegOutcomeFailed
	record.Legs[0].Evidence.InputTokens = Quantity{}
	record.Legs[0].Evidence.OutputTokens = Quantity{}
	policy := ratingPolicy(ChargeAllPotentialLegs)
	record.ChargePolicyRef = policy.Ref
	auth := Authorization{
		ID: "auth-1", AccountID: "acct-1", TURKey: "acct-1:turn-1",
		Amount: Money{Nano: 1000, Currency: "USD"}, PricingRef: ratingPricing().Ref, ChargePolicyRef: policy.Ref,
	}
	result, err := RateTurn(RatingInput{
		Record: record, Authorization: auth, CustomerPricing: ratingPricing(),
		CustomerPolicy: policy, OperatorRates: []OperatorRateSnapshot{operatorRate()},
	})
	if err != nil {
		t.Fatalf("completed pass-through must not fail closed on a rejected sibling: %v", err)
	}
	if result.CustomerCharge.Nano != 303 || result.UnreconciledCost {
		t.Fatalf("customer charge = %+v, want winner-only 303 and reconciled", result)
	}
	if len(result.OperatorCosts) != 2 || !result.OperatorCosts[0].Reconciled || result.OperatorCosts[0].Amount.Nano != 0 || !result.OperatorCosts[1].Authoritative {
		t.Fatalf("operator costs = %+v, want rejected $0 plus authoritative winner", result.OperatorCosts)
	}
}

func TestRateTurnCompletedAllPotentialLegsFailedSiblingBillsPresentInput(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedNo, SurfacedYes}, []MoneyEvidence{
		{Currency: "USD", Present: false}, {NanoUnits: 0, Currency: "USD", Present: true},
	})
	record.Legs[0].Outcome = LegOutcomeFailed
	record.Legs[0].Evidence.OutputTokens = Quantity{}
	policy := ratingPolicy(ChargeAllPotentialLegs)
	record.ChargePolicyRef = policy.Ref
	auth := Authorization{
		ID: "auth-1", AccountID: "acct-1", TURKey: "acct-1:turn-1",
		Amount: Money{Nano: 1000, Currency: "USD"}, PricingRef: ratingPricing().Ref, ChargePolicyRef: policy.Ref,
	}
	result, err := RateTurn(RatingInput{
		Record: record, Authorization: auth, CustomerPricing: ratingPricing(),
		CustomerPolicy: policy, OperatorRates: []OperatorRateSnapshot{operatorRate()},
	})
	if err != nil {
		t.Fatalf("completed pass-through must skip missing output on a failed sibling: %v", err)
	}
	// Winner 303 (input+output+fixed) + sibling input 100 + sibling fixed 3.
	if result.CustomerCharge.Nano != 406 || result.UnreconciledCost {
		t.Fatalf("customer charge = %+v, want 406 (winner plus sibling input/fixed)", result)
	}
}

func TestRateTurnAbsentCostUsesExactOperatorRateAndAuthoritativeZeroDoesNot(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedYes, SurfacedNo}, []MoneyEvidence{
		{Currency: "USD", Present: false}, {NanoUnits: 0, Currency: "USD", Present: true},
	})
	result, err := RateTurn(RatingInput{Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(), CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{operatorRate()}})
	if err != nil {
		t.Fatal(err)
	}
	if result.UnreconciledCost || !result.OperatorCosts[0].AmountPresent || !result.OperatorCosts[1].AmountPresent || result.OperatorCosts[0].Amount.Nano != 125 || result.OperatorCosts[1].Amount.Nano != 0 {
		t.Fatalf("fallback/zero result = %+v", result)
	}
	missingRate := operatorRate()
	missingRate.Ref.Version = "other"
	result, err = RateTurn(RatingInput{Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(), CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{missingRate}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.UnreconciledCost || len(result.UnreconciledLURKeys) != 1 || result.OperatorCosts[0].AmountPresent || result.OperatorCosts[0].Reconciled {
		t.Fatalf("unreconciled result = %+v", result)
	}
}

func TestRateTurnRejectsExactSnapshotMismatchesBeforeRating(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedYes}, []MoneyEvidence{{NanoUnits: 0, Currency: "USD", Present: true}})
	pricing := ratingPricing()
	pricing.Ref.Version = "different"
	if _, err := RateTurn(RatingInput{Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: pricing, CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{operatorRate()}}); !errors.Is(err, ErrRatingSnapshotMismatch) {
		t.Fatalf("pricing mismatch = %v", err)
	}
	policy := ratingPolicy(ChargeSurfacedTurn)
	policy.Ref.Version = "different"
	if _, err := RateTurn(RatingInput{Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(), CustomerPolicy: policy, OperatorRates: []OperatorRateSnapshot{operatorRate()}}); !errors.Is(err, ErrRatingSnapshotMismatch) {
		t.Fatalf("policy mismatch = %v", err)
	}
}

type ratingProcessingMarker struct{ called bool }

func (m *ratingProcessingMarker) MarkProcessingUnreconciledCost(context.Context, string, string, string) error {
	m.called = true
	return nil
}

func TestMarkUnreconciledCostUsesMutableProcessingOnly(t *testing.T) {
	t.Parallel()
	marker := &ratingProcessingMarker{}
	record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedYes}, []MoneyEvidence{{Currency: "USD", Present: false}})
	result, err := RateTurn(RatingInput{Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(), CustomerPolicy: ratingPolicy(ChargeSurfacedTurn)})
	if err != nil {
		t.Fatal(err)
	}
	if err := MarkUnreconciledCost(context.Background(), marker, record, result); err != nil {
		t.Fatal(err)
	}
	if !marker.called {
		t.Fatal("processing marker was not called")
	}
}

func TestRateTurnIgnoresOptionalRateDimensionsWithoutQuantities(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedYes}, []MoneyEvidence{{Currency: "USD", Present: false}})
	rate := operatorRate()
	rate.CacheReadRatePresent = true
	rate.CacheReadPerMillionNano = 40
	rate.CacheWriteRatePresent = true
	rate.CacheWritePerMillionNano = 80
	rate.ReasoningRatePresent = true
	rate.ReasoningPerMillionNano = 90
	result, err := RateTurn(RatingInput{
		Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(),
		CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{rate},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.UnreconciledCost || !result.OperatorCosts[0].AmountPresent || result.OperatorCosts[0].Amount.Nano != 125 {
		t.Fatalf("optional absent quantities must not unreconcile; got %+v", result)
	}
}

func TestRateTurnPresentQuantityWithoutRateIsUnreconciled(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedYes}, []MoneyEvidence{{Currency: "USD", Present: false}})
	record.Legs[0].Evidence.CacheReadTokens = Quantity{Value: 1_000_000, Present: true}
	rate := operatorRate() // no cache rate
	result, err := RateTurn(RatingInput{
		Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(),
		CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{rate},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.UnreconciledCost || result.OperatorCosts[0].Reconciled || result.OperatorCosts[0].UnreconciledReason != "operator_rate_or_quantity_incomplete" {
		t.Fatalf("present cache quantity without rate must unreconcile; got %+v", result)
	}
}

func TestRateTurnPresentZeroCostWithoutAuthorityFallsBack(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedYes}, []MoneyEvidence{{NanoUnits: 0, Currency: "USD", Present: true}})
	record.Legs[0].Evidence.Authority = ""
	result, err := RateTurn(RatingInput{
		Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(),
		CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{operatorRate()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.UnreconciledCost || result.OperatorCosts[0].Authoritative || result.OperatorCosts[0].Amount.Nano != 125 {
		t.Fatalf("present-zero cost without authority must fall back; got %+v", result)
	}
}

func TestRateTurnEstimatedCostDoesNotBecomeAuthoritativeCOGS(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedYes}, []MoneyEvidence{{NanoUnits: 999, Currency: "USD", Present: true}})
	record.Legs[0].Evidence.Authority = EvidenceAuthorityEstimated
	record.Legs[0].Evidence.Source = EvidenceSourceLocalEstimator
	result, err := RateTurn(RatingInput{
		Record: record, Authorization: ratingAuthorization(1000), CustomerPricing: ratingPricing(),
		CustomerPolicy: ratingPolicy(ChargeSurfacedTurn), OperatorRates: []OperatorRateSnapshot{operatorRate()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.UnreconciledCost || result.OperatorCosts[0].Authoritative || result.OperatorCosts[0].Amount.Nano != 125 {
		t.Fatalf("estimated present cost must fall back to operator rate; got %+v", result)
	}
}

func TestRateTurnUsesExactFloorTokenArithmetic(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedYes}, []MoneyEvidence{{Currency: "USD", Present: false}})
	record.Legs[0].Evidence.InputTokens = Quantity{Value: 1, Present: true}
	record.Legs[0].Evidence.OutputTokens = Quantity{Value: 1, Present: true}
	pricing := ratingPricing()
	pricing.FixedCharges = nil
	policy := ratingPolicy(ChargeSurfacedTurn)
	policy.IncludeFixedCharges = false
	auth := ratingAuthorization(1000)
	auth.ChargePolicyRef = policy.Ref
	record.ChargePolicyRef = policy.Ref
	result, err := RateTurn(RatingInput{
		Record: record, Authorization: auth, CustomerPricing: pricing, CustomerPolicy: policy,
		OperatorRates: []OperatorRateSnapshot{operatorRate()},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 1 token at 100 nano/M and 1 at 200 nano/M floor to 0; admission ceiling would charge 2.
	if result.CustomerCharge.Nano != 0 {
		t.Fatalf("customer charge = %d, want exact floor 0 (not admission ceiling)", result.CustomerCharge.Nano)
	}
	if result.OperatorCosts[0].Amount.Nano != 0 {
		t.Fatalf("operator cost = %d, want exact floor 0", result.OperatorCosts[0].Amount.Nano)
	}
}

func TestRateTurnUsesPerModelCustomerCards(t *testing.T) {
	t.Parallel()
	record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedNo, SurfacedYes}, []MoneyEvidence{
		{NanoUnits: 0, Currency: "USD", Present: true}, {NanoUnits: 0, Currency: "USD", Present: true},
	})
	record.Legs[0].ModelID = "cheap"
	record.Legs[1].ModelID = "expensive"
	cheap := ratingPricing()
	cheap.InputPerMillionNano = 10
	cheap.OutputPerMillionNano = 10
	cheap.FixedCharges = nil
	expensive := ratingPricing()
	expensive.InputPerMillionNano = 100
	expensive.OutputPerMillionNano = 200
	expensive.FixedCharges = nil
	policy := ratingPolicy(ChargeAllPotentialLegs)
	policy.IncludeFixedCharges = false
	record.ChargePolicyRef = policy.Ref
	auth := ratingAuthorization(1000)
	auth.ChargePolicyRef = policy.Ref
	result, err := RateTurn(RatingInput{
		Record: record, Authorization: auth, CustomerPricing: ratingPricing(),
		ModelPricing: []ModelCustomerPricing{
			{BackendID: "backend", ModelID: "cheap", Pricing: cheap},
			{BackendID: "backend", ModelID: "expensive", Pricing: expensive},
		},
		CustomerPolicy: policy, OperatorRates: []OperatorRateSnapshot{operatorRate()},
	})
	if err != nil {
		t.Fatal(err)
	}
	// cheap 10+10 plus expensive 100+200.
	if result.CustomerCharge.Nano != 320 {
		t.Fatalf("heterogeneous customer charge = %d, want 320", result.CustomerCharge.Nano)
	}
}
