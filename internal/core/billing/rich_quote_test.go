package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 14.1 RED: quote token and non-token exposure from the same policy
// semantics used for settlement. These tests must fail before implementation
// (undefined EstimateRichCustomerCharge) and pass after.

func richQuoteTariff(t *testing.T) economics.TariffSnapshot {
	t.Helper()
	textKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}
	audioKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentAudio, Unit: metering.UnitSecond, SchemaID: "retail.v1"}
	toolKey := metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentToolQuery, Unit: metering.UnitCount, SchemaID: "retail.v1"}
	creditKey := metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentCredit, Unit: metering.UnitCredit, SchemaID: "retail.v1"}
	storageKey := metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentStorage, Unit: metering.UnitByteSecond, SchemaID: "retail.v1"}
	mustDecimal := func(value string) *metering.Decimal {
		d, err := metering.ParseDecimal(value)
		if err != nil {
			t.Fatal(err)
		}
		return &d
	}
	rules := []economics.RatingRule{
		{ID: "text-input", Kind: economics.RatingRuleLinear, Component: &textKey, Currency: "USD", UnitPrice: mustDecimal("0.000000001")},
		{ID: "call-fee", Kind: economics.RatingRuleFixed, Currency: "USD", FixedAmount: mustDecimal("0.000000005"), FixedScope: economics.FixedFeeScopeCall},
		{ID: "audio-min", Kind: economics.RatingRuleMinimum, Component: &audioKey, Currency: "USD", UnitPrice: mustDecimal("0.000000001"), MinimumAmount: mustDecimal("0.000000010")},
		{ID: "tool", Kind: economics.RatingRuleLinear, Component: &toolKey, Currency: "USD", UnitPrice: mustDecimal("0.000000002")},
		{ID: "credit", Kind: economics.RatingRuleLinear, Component: &creditKey, Currency: "USD", UnitPrice: mustDecimal("0.000000001")},
		{ID: "storage", Kind: economics.RatingRuleLinear, Component: &storageKey, Currency: "USD", UnitPrice: mustDecimal("0.000000001")},
	}
	return phase10RetailTariff(t, rules)
}

func richQuoteBounds(t *testing.T) []RichComponentBound {
	t.Helper()
	mustKey := func(direction metering.FlowDirection, component, unit string) metering.ComponentKey {
		return metering.ComponentKey{Direction: direction, Component: component, Unit: unit, SchemaID: "retail.v1"}
	}
	mustUpper := func(value string) metering.Decimal {
		d, err := metering.ParseDecimal(value)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	return []RichComponentBound{
		{Key: mustKey(metering.DirectionInput, metering.ComponentTextToken, metering.UnitToken), Upper: mustUpper("100"), Enforceable: true},
		{Key: mustKey(metering.DirectionInput, metering.ComponentAudio, metering.UnitSecond), Upper: mustUpper("2"), Enforceable: true},
		{Key: mustKey(metering.DirectionNone, metering.ComponentToolQuery, metering.UnitCount), Upper: mustUpper("3"), Enforceable: true},
		{Key: mustKey(metering.DirectionNone, metering.ComponentCredit, metering.UnitCredit), Upper: mustUpper("4"), Enforceable: true},
		{Key: mustKey(metering.DirectionNone, metering.ComponentStorage, metering.UnitByteSecond), Upper: mustUpper("7"), Enforceable: true},
	}
}

func richQuotePolicy(tariff economics.TariffSnapshot) ChargePolicy {
	return ChargePolicy{
		Ref:                    VersionRef{ID: "retail-policy", Version: "v10"},
		PricingRef:             VersionRef{ID: tariff.Ref.ID, Version: tariff.Ref.Version},
		Scope:                  ChargeSurfacedTurn,
		IncludeInputTokens:     true,
		IncludeFixedCharges:    true,
		IncludeResourceCharges: true,
		Retail:                 &RetailSelectionPolicy{Mode: RetailSelectionSurfacedWinner, Basis: RetailBasisIndependent},
	}
}

func TestRichQuoteCoversUnitFixedMinimumCreditResource(t *testing.T) {
	t.Parallel()
	tariff := richQuoteTariff(t)
	policy := richQuotePolicy(tariff)
	bound, err := EstimateRichCustomerCharge(RichQuoteInput{
		Currency: "USD", Policy: policy, BaseTariff: tariff,
		Bounds: richQuoteBounds(t),
		Routes: []RichQuoteRoute{{ID: "a:m1", Backend: "a", Model: "m1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 100 text * 1 + 5 call fee + max(2*1, 10) audio + 3*2 tool + 4*1 credit + 7*1 storage = 132 nano.
	if bound.Amount.Nano != 132 || bound.Amount.Currency != "USD" {
		t.Fatalf("bound = %+v, want 132 USD nano", bound.Amount)
	}
	if bound.PricingRef.ID != tariff.Ref.ID || bound.PricingRef.Version != tariff.Ref.Version {
		t.Fatalf("pricing ref = %+v, want tariff %v", bound.PricingRef, tariff.Ref)
	}
	if bound.ChargePolicyRef != policy.Ref {
		t.Fatalf("policy ref = %+v, want %+v", bound.ChargePolicyRef, policy.Ref)
	}
}

func TestRichQuoteDeniesUnknownDurationWithoutEnforceableBound(t *testing.T) {
	t.Parallel()
	tariff := richQuoteTariff(t)
	policy := richQuotePolicy(tariff)
	bounds := richQuoteBounds(t)
	// Drop the audio duration bound to simulate unknown duration.
	filtered := make([]RichComponentBound, 0, len(bounds)-1)
	for _, b := range bounds {
		if b.Key.Component == metering.ComponentAudio {
			continue
		}
		filtered = append(filtered, b)
	}
	if _, err := EstimateRichCustomerCharge(RichQuoteInput{
		Currency: "USD", Policy: policy, BaseTariff: tariff,
		Bounds: filtered, Routes: []RichQuoteRoute{{ID: "a:m1", Backend: "a", Model: "m1"}},
	}); !errors.Is(err, ErrEstimateUnbounded) {
		t.Fatalf("unknown duration error = %v, want ErrEstimateUnbounded", err)
	}
	// BLOCKER 2: a bare configured number proves no contractual liability or
	// enforceable work bound, so it must not convert the denial into admission.
	// The rich path takes no Strict/ceiling fallback by design.
	if _, err := EstimateRichCustomerCharge(RichQuoteInput{
		Currency: "USD", Policy: policy, BaseTariff: tariff,
		Bounds: filtered, Routes: []RichQuoteRoute{{ID: "a:m1", Backend: "a", Model: "m1"}},
	}); !errors.Is(err, ErrEstimateUnbounded) {
		t.Fatalf("unknown duration with bare ceiling configured = %v, want ErrEstimateUnbounded", err)
	}
}

func TestRichQuoteDeniesMissingEvidenceCapability(t *testing.T) {
	t.Parallel()
	tariff := richQuoteTariff(t)
	policy := richQuotePolicy(tariff)
	in := RichQuoteInput{
		Currency: "USD", Policy: policy, BaseTariff: tariff,
		Bounds:               richQuoteBounds(t),
		Routes:               []RichQuoteRoute{{ID: "a:m1", Backend: "a", Model: "m1", Capabilities: []string{"text"}}},
		RequiredCapabilities: []string{"tool_count"},
	}
	if _, err := EstimateRichCustomerCharge(in); !errors.Is(err, ErrEstimateUnbounded) {
		t.Fatalf("missing capability error = %v, want ErrEstimateUnbounded", err)
	}
	// BLOCKER 2: a bare configured number is not an enforceable bound. The
	// rich path takes no Strict/ceiling fallback by design.
	if _, err := EstimateRichCustomerCharge(in); !errors.Is(err, ErrEstimateUnbounded) {
		t.Fatalf("missing capability with bare ceiling configured = %v, want ErrEstimateUnbounded", err)
	}
}

func TestRichQuoteAgreesWithSettlementOnScopeRoundingPriceVersion(t *testing.T) {
	t.Parallel()
	tariff := richQuoteTariff(t)
	policy := richQuotePolicy(tariff)
	quote, err := EstimateRichCustomerCharge(RichQuoteInput{
		Currency: "USD", Policy: policy, BaseTariff: tariff,
		Bounds: richQuoteBounds(t),
		Routes: []RichQuoteRoute{{ID: "a:m1", Backend: "a", Model: "m1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	call := retailSelectionCall(t, policy, "b-winner")
	observation := phase10RetailObservation(t, call.CallID, "b-winner", "winner-usage",
		metering.OriginProvider, metering.BoundaryBackendIngress,
		phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}, quantity: "100"},
		phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentAudio, Unit: metering.UnitSecond, SchemaID: "retail.v1"}, quantity: "2"},
		phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentToolQuery, Unit: metering.UnitCount, SchemaID: "retail.v1"}, quantity: "3"},
		phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentCredit, Unit: metering.UnitCredit, SchemaID: "retail.v1"}, quantity: "4"},
		phase10RetailMeasure{key: metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentStorage, Unit: metering.UnitByteSecond, SchemaID: "retail.v1"}, quantity: "7"},
	)
	leg := phase10RetailLeg(t, call.CallID, "b-winner", 1, LegOutcomeWinner, SurfacedYes, observation)
	selection, err := SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: []CallLegUsageRecord{leg}, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	settled, err := RateSelectedRetailBLegs(context.Background(), RetailRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, Selection: selection,
		Policy: policy, Tariff: tariff,
		Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	})
	if err != nil {
		t.Fatalf("settlement: %v", err)
	}
	if settled.CustomerCharge != quote.Amount {
		t.Fatalf("quote %+v != settlement %+v; scope/rounding/price version must agree", quote.Amount, settled.CustomerCharge)
	}
	if settled.Valuation.Tariff.ID != quote.PricingRef.ID || settled.Valuation.Tariff.Version != quote.PricingRef.Version {
		t.Fatalf("settlement tariff %v != quote pricing %v", settled.Valuation.Tariff, quote.PricingRef)
	}
}
