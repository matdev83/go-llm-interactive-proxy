package billing

import (
	"errors"
	"testing"
)

// TestPhase18TokenOnlyFallbackIsUnreconciled is the Task 18.1 RED contract:
// provider-accepted token-only evidence without provider-reported money must
// not be converted into live money by a scalar per-million fallback. The V2
// provider-quantity valuation owns estimates; the V1 scalar fallback is
// retired per Migration Strategy step 8.
func TestPhase18TokenOnlyFallbackIsUnreconciled(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	leg := testCallLegUsageRecord(callID, "b-phase18-token-only")
	leg.Evidence.Cost = MoneyEvidence{}
	leg.OperatorRateRef = operatorRate().Ref
	_, err := RateProviderCost(leg, OperatorRateSet{operatorRate()}, "USD")
	if !errors.Is(err, ErrUnreconciledCost) {
		t.Fatalf("RateProviderCost(token-only) = %v, want %v", err, ErrUnreconciledCost)
	}
}
