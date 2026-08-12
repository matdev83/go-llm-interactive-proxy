package billing

import (
	"fmt"
	"testing"

	legacyaccounting "github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
)

func TestPhase7_DeterministicShadowRatingAcceptanceGate(t *testing.T) {
	t.Parallel()
	basePricing := ratingPricing()
	basePolicy := ratingPolicy(ChargeSurfacedTurn)
	rate := operatorRate()
	// Customer charges are frozen characterization literals for the sealed
	// fixtures below. They must not call RateTurn helpers: if rating drifts,
	// this suite must fail instead of rewriting the baseline in lockstep.
	const surfacedLegCustomer = int64(303) // 1M*100 + 1M*200 + fixed 3
	cases := []struct {
		name     string
		input    RatingInput
		expected ShadowExpectation
	}{
		{
			name:  "success",
			input: RatingInput{Record: ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedYes}, []MoneyEvidence{{NanoUnits: 7, Currency: "USD", Present: true}}), Authorization: ratingAuthorization(1000), CustomerPricing: basePricing, CustomerPolicy: basePolicy, OperatorRates: []OperatorRateSnapshot{rate}},
			expected: ShadowExpectation{
				CustomerCharge: Money{Nano: surfacedLegCustomer, Currency: "USD"},
				ProviderCosts:  map[string]Money{"acct-1:turn-1:b-1": {Nano: 7, Currency: "USD"}},
			},
		},
		{
			name:  "failover",
			input: RatingInput{Record: ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedNo, SurfacedYes}, []MoneyEvidence{{Currency: "USD"}, {NanoUnits: 0, Currency: "USD", Present: true}}), Authorization: ratingAuthorization(1000), CustomerPricing: basePricing, CustomerPolicy: basePolicy, OperatorRates: []OperatorRateSnapshot{rate}},
			expected: ShadowExpectation{
				CustomerCharge: Money{Nano: surfacedLegCustomer, Currency: "USD"},
				ProviderCosts: map[string]Money{
					"acct-1:turn-1:b-1": {Nano: mustLegacyOperatorCost(t, ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedNo, SurfacedYes}, []MoneyEvidence{{Currency: "USD"}, {NanoUnits: 0, Currency: "USD", Present: true}}).Legs[0], rate), Currency: "USD"},
					"acct-1:turn-1:b-2": {Nano: 0, Currency: "USD"},
				},
			},
		},
		{
			name: "parallel-all-chargeable",
			input: func() RatingInput {
				record := ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedNo, SurfacedYes}, []MoneyEvidence{{NanoUnits: 0, Currency: "USD", Present: true}, {NanoUnits: 0, Currency: "USD", Present: true}})
				policy := ratingPolicy(ChargeAllPotentialLegs)
				record.ChargePolicyRef = policy.Ref
				auth := ratingAuthorization(1000)
				auth.ChargePolicyRef = policy.Ref
				return RatingInput{Record: record, Authorization: auth, CustomerPricing: basePricing, CustomerPolicy: policy, OperatorRates: []OperatorRateSnapshot{rate}}
			}(),
			expected: ShadowExpectation{
				CustomerCharge: Money{Nano: surfacedLegCustomer * 2, Currency: "USD"},
				ProviderCosts: map[string]Money{
					"acct-1:turn-1:b-1": {Nano: 0, Currency: "USD"},
					"acct-1:turn-1:b-2": {Nano: 0, Currency: "USD"},
				},
			},
		},
		{
			name:  "cancel",
			input: RatingInput{Record: ratingRecord(TurnOutcomeCanceled, []SurfacedState{SurfacedNo}, []MoneyEvidence{{NanoUnits: 4, Currency: "USD", Present: true}}), Authorization: ratingAuthorization(1000), CustomerPricing: basePricing, CustomerPolicy: basePolicy, OperatorRates: []OperatorRateSnapshot{rate}},
			expected: ShadowExpectation{
				// Cancel after provider acceptance bills observed usage (OpenRouter-style).
				CustomerCharge: Money{Nano: surfacedLegCustomer, Currency: "USD"},
				ProviderCosts:  map[string]Money{"acct-1:turn-1:b-1": {Nano: 4, Currency: "USD"}},
			},
		},
		{
			name:  "authoritative-zero",
			input: RatingInput{Record: ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedYes}, []MoneyEvidence{{NanoUnits: 0, Currency: "USD", Present: true}}), Authorization: ratingAuthorization(1000), CustomerPricing: basePricing, CustomerPolicy: basePolicy, OperatorRates: []OperatorRateSnapshot{rate}},
			expected: ShadowExpectation{
				CustomerCharge: Money{Nano: surfacedLegCustomer, Currency: "USD"},
				ProviderCosts:  map[string]Money{"acct-1:turn-1:b-1": {Nano: 0, Currency: "USD"}},
			},
		},
		{
			name:  "absent-provider-cost",
			input: RatingInput{Record: ratingRecord(TurnOutcomeCompleted, []SurfacedState{SurfacedYes}, []MoneyEvidence{{Currency: "USD", Present: false}}), Authorization: ratingAuthorization(1000), CustomerPricing: basePricing, CustomerPolicy: basePolicy, OperatorRates: nil},
			expected: ShadowExpectation{
				CustomerCharge:   Money{Nano: surfacedLegCustomer, Currency: "USD"},
				ProviderCosts:    map[string]Money{"acct-1:turn-1:b-1": {Currency: "USD"}},
				UnreconciledCost: true,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Provider-cost legs that require rate fallback are characterized through
			// the real legacy EstimateCost path, not RateTurn's fallback helper.
			comparison, err := CompareShadowRating(tc.input, tc.expected)
			if err != nil {
				t.Fatal(err)
			}
			if !comparison.Matches {
				t.Fatalf("shadow mismatch: %v", comparison.Differences)
			}
		})
	}
}

func mustLegacyOperatorCost(t *testing.T, leg LegUsageRecord, rate OperatorRateSnapshot) int64 {
	t.Helper()
	catalog, err := legacyaccounting.NewPriceCatalog(legacyaccounting.PriceCatalogConfig{
		Version:  rate.Ref.Version,
		Currency: rate.Currency,
		Models: []legacyaccounting.ModelPriceConfig{{
			Backend:              leg.BackendID,
			Model:                leg.ModelID,
			InputPer1M:           legacyNanoRateDecimal(rate.InputPerMillionNano),
			CachedInputPer1M:     legacyOptionalNanoRateDecimal(rate.CacheReadPerMillionNano, rate.CacheReadRatePresent),
			CacheWriteInputPer1M: legacyOptionalNanoRateDecimal(rate.CacheWritePerMillionNano, rate.CacheWriteRatePresent),
			OutputPer1M:          legacyNanoRateDecimal(rate.OutputPerMillionNano),
			ReasoningOutputPer1M: legacyOptionalNanoRateDecimal(rate.ReasoningPerMillionNano, rate.ReasoningRatePresent),
		}},
	})
	if err != nil {
		t.Fatalf("legacy catalog: %v", err)
	}
	result := legacyaccounting.EstimateCost(legacyaccounting.CostInput{
		Backend: leg.BackendID,
		Model:   leg.ModelID,
		Usage: legacyaccounting.TokenUsage{
			InputTokens:      leg.Evidence.InputTokens.Value,
			OutputTokens:     leg.Evidence.OutputTokens.Value,
			CacheReadTokens:  leg.Evidence.CacheReadTokens.Value,
			CacheWriteTokens: leg.Evidence.CacheWriteTokens.Value,
			ReasoningTokens:  leg.Evidence.ReasoningTokens.Value,
		},
	}, catalog)
	if result.Unavailable {
		t.Fatal("legacy EstimateCost unavailable for characterization fixture")
	}
	return result.NanoUnits
}

func legacyNanoRateDecimal(rate int64) string {
	return fmt.Sprintf("%d.%09d", rate/1_000_000_000, rate%1_000_000_000)
}

func legacyOptionalNanoRateDecimal(rate int64, present bool) string {
	if !present {
		return ""
	}
	return legacyNanoRateDecimal(rate)
}
