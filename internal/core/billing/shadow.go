package billing

import "fmt"

// ShadowExpectation is the characterization baseline for one representative
// financial scenario. It contains only deterministic monetary outcomes, never
// legacy raw events or provider payloads.
type ShadowExpectation struct {
	CustomerCharge   Money
	ProviderCosts    map[string]Money
	UnreconciledCost bool
}

// ShadowBaseline is the deterministic characterization seam for the financial
// behavior that existed before TUR rating became authoritative. The baseline is
// deliberately supplied by the migration boundary: core billing does not import
// stream, metering, provider, or legacy ledger implementations.
type ShadowBaseline func(RatingInput) (ShadowExpectation, error)

// ShadowComparison records whether the replacement TUR/rating path agrees with
// the characterized financial outcome.
type ShadowComparison struct {
	Result      BillingResult
	Expected    ShadowExpectation
	Matches     bool
	Differences []string
}

// CompareShadowRatingAgainst runs the new pure rating path against an executable
// characterization baseline. It is intentionally observational and cannot post
// money or mutate runtime.
func CompareShadowRatingAgainst(input RatingInput, baseline ShadowBaseline) (ShadowComparison, error) {
	if baseline == nil {
		return ShadowComparison{}, fmt.Errorf("billing: shadow baseline is required")
	}
	expected, err := baseline(input)
	if err != nil {
		return ShadowComparison{}, fmt.Errorf("billing: shadow baseline: %w", err)
	}
	result, err := RateTurn(input)
	if err != nil {
		return ShadowComparison{}, err
	}
	comparison := ShadowComparison{Result: result, Expected: expected, Matches: true}
	if result.CustomerCharge != expected.CustomerCharge {
		comparison.Matches = false
		comparison.Differences = append(comparison.Differences, fmt.Sprintf("customer charge got %v want %v", result.CustomerCharge, expected.CustomerCharge))
	}
	if result.UnreconciledCost != expected.UnreconciledCost {
		comparison.Matches = false
		comparison.Differences = append(comparison.Differences, fmt.Sprintf("unreconciled cost got %t want %t", result.UnreconciledCost, expected.UnreconciledCost))
	}
	if len(result.OperatorCosts) != len(expected.ProviderCosts) {
		comparison.Matches = false
		comparison.Differences = append(comparison.Differences, fmt.Sprintf("provider cost count got %d want %d", len(result.OperatorCosts), len(expected.ProviderCosts)))
	}
	for _, cost := range result.OperatorCosts {
		want, ok := expected.ProviderCosts[cost.LURKey]
		if !ok || cost.Amount != want {
			comparison.Matches = false
			comparison.Differences = append(comparison.Differences, fmt.Sprintf("provider cost %s got %v want %v", cost.LURKey, cost.Amount, want))
		}
	}
	return comparison, nil
}

// CompareShadowRating preserves the small value-based characterization helper
// for callers that already have a captured expected result. New migration tests
// should prefer CompareShadowRatingAgainst so the baseline is executable.
func CompareShadowRating(input RatingInput, expected ShadowExpectation) (ShadowComparison, error) {
	return CompareShadowRatingAgainst(input, func(RatingInput) (ShadowExpectation, error) {
		return expected, nil
	})
}
