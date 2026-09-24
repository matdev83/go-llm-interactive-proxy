package billing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

func phase10CostPassThroughPolicy(t *testing.T, missing CostPassThroughMissingCostPolicy, allowLate bool) ChargePolicy {
	t.Helper()
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisCostPassThrough)
	retail := policy.Retail.Clone()
	retail.CostPassThrough = &CostPassThroughPolicy{
		MissingCost:         missing,
		SafeBound:           &Money{Nano: 100, Currency: "USD"},
		AllowLateAdjustment: allowLate,
	}
	policy.Retail = &retail
	return policy
}

func phase10CostPassThroughRatingInput(t *testing.T, policy ChargePolicy) (RetailRatingInput, RetailSelectionResult) {
	t.Helper()
	call := retailSelectionCall(t, policy, "b-winner")
	leg := phase10RetailLeg(t, call.CallID, "b-winner", 1, LegOutcomeWinner, SurfacedYes,
		phase10RetailObservation(t, call.CallID, "b-winner", "pass-through-usage", metering.OriginProvider, metering.BoundaryBackendIngress,
			phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}, quantity: "1"},
		))
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: []CallLegUsageRecord{leg}, Policy: policy})
	require.NoError(t, err)
	return RetailRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, Selection: selection, Policy: policy,
		Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	}, selection
}

func TestPhase10CostPassThroughMissingCostUsesBoundedProvisionalState(t *testing.T) {
	t.Parallel()
	policy := phase10CostPassThroughPolicy(t, CostPassThroughMissingCostProvisional, true)
	in, _ := phase10CostPassThroughRatingInput(t, policy)
	got, err := RateSelectedRetailBLegs(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, Money{Nano: 100, Currency: "USD"}, got.CustomerCharge)
	require.NotNil(t, got.CostPassThrough)
	require.Equal(t, CostPassThroughSettlementProvisional, got.CostPassThrough.Status)
	require.Equal(t, Money{Nano: 100, Currency: "USD"}, got.CostPassThrough.SafeBound)
	require.Equal(t, Money{Nano: 100, Currency: "USD"}, got.CostPassThrough.PostedAmount)
	require.True(t, got.CostPassThrough.Policy.AllowLateAdjustment)

	provider := CostPassThroughProviderCost{
		LURKey: "lur-1", ValuationID: "valuation-2", Revision: 2,
		InputHash: strings.Repeat("a", 64), Amount: Money{Nano: 80, Currency: "USD"},
		AmountPresent: true, Reconciled: true, Authoritative: true,
	}
	in.ProviderCost = &provider
	final, err := RateSelectedRetailBLegs(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, Money{Nano: 80, Currency: "USD"}, final.CustomerCharge)
	require.Equal(t, CostPassThroughSettlementFinal, final.CostPassThrough.Status)
	require.Equal(t, uint64(2), final.CostPassThrough.ProviderCost.Revision)
	require.Equal(t, provider.InputHash, final.CostPassThrough.ProviderCost.InputHash)
}

func TestPhase10RateCallPassThroughUsesAdmittedBoundWithoutCustomerTariff(t *testing.T) {
	t.Parallel()
	policy := phase10CostPassThroughPolicy(t, CostPassThroughMissingCostProvisional, true)
	in, _ := phase10CostPassThroughRatingInput(t, policy)
	got, err := RateCall(CallRatingInput{
		Call: in.Call, Legs: in.Legs, MaxCustomerCharge: Money{Nano: 100, Currency: "USD"},
		CustomerPricing: PricingSnapshot{Currency: "EUR"}, CustomerPolicy: policy,
	})
	require.NoError(t, err)
	require.Equal(t, Money{Nano: 100, Currency: "USD"}, got.CustomerCharge)
	require.NotNil(t, got.CostPassThrough)
	require.Equal(t, CostPassThroughSettlementProvisional, got.CostPassThrough.Status)
}

func TestPhase10CostPassThroughSafeBoundOwnsAdmissionEstimate(t *testing.T) {
	t.Parallel()
	policy := phase10CostPassThroughPolicy(t, CostPassThroughMissingCostPending, true)
	policy.IncludeInputTokens = false
	policy.IncludeOutputTokens = false
	policy.IncludeFixedCharges = false
	policy.IncludeResourceCharges = false
	bound, err := EstimateMaxCustomerCharge(MaxChargeInput{
		Currency: "USD", InputTokens: 0, InputTokensPresent: false, Policy: policy,
	})
	require.NoError(t, err)
	require.Equal(t, Money{Nano: 100, Currency: "USD"}, bound.Amount)
	require.Len(t, bound.Basis, 1)
	require.Equal(t, "cost_pass_through_safe_bound", bound.Basis[0].Kind)
}

func TestPhase10CostPassThroughMissingCostCanRemainPendingWithoutLateAdjustment(t *testing.T) {
	t.Parallel()
	policy := phase10CostPassThroughPolicy(t, CostPassThroughMissingCostPending, false)
	in, _ := phase10CostPassThroughRatingInput(t, policy)
	got, err := RateSelectedRetailBLegs(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, Money{Nano: 0, Currency: "USD"}, got.CustomerCharge)
	require.NotNil(t, got.CostPassThrough)
	require.Equal(t, CostPassThroughSettlementPending, got.CostPassThrough.Status)
	require.Equal(t, Money{Nano: 100, Currency: "USD"}, got.CostPassThrough.SafeBound)
	require.Equal(t, Money{Nano: 0, Currency: "USD"}, got.CostPassThrough.PostedAmount)
	require.False(t, got.CostPassThrough.Policy.AllowLateAdjustment)
}

func TestPhase10CostPassThroughRejectsUntrustedOrIncomparableProviderCost(t *testing.T) {
	t.Parallel()
	policy := phase10CostPassThroughPolicy(t, CostPassThroughMissingCostPending, true)
	in, _ := phase10CostPassThroughRatingInput(t, policy)
	provider := CostPassThroughProviderCost{
		LURKey: "lur-1", ValuationID: "valuation-2", Revision: 2,
		InputHash: strings.Repeat("b", 64), Amount: Money{Nano: 80, Currency: "EUR"},
		AmountPresent: true, Reconciled: true, Authoritative: false,
	}
	in.ProviderCost = &provider
	_, err := RateSelectedRetailBLegs(context.Background(), in)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrCostPassThroughProviderUntrusted), err)

	provider.Authoritative = true
	_, err = RateSelectedRetailBLegs(context.Background(), in)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrCostPassThroughCurrencyMismatch), err)
}

func TestPhase10IndependentRetailRejectsEmbeddedPassThroughPolicy(t *testing.T) {
	t.Parallel()
	policy := retailSelectionPolicy(RetailSelectionSurfacedWinner, RetailBasisIndependent)
	retail := policy.Retail.Clone()
	retail.CostPassThrough = &CostPassThroughPolicy{MissingCost: CostPassThroughMissingCostProvisional, SafeBound: &Money{Nano: 100, Currency: "USD"}}
	policy.Retail = &retail
	err := policy.Validate()
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrCostPassThroughPolicyInvalid), err)
}
