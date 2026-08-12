package billing

import (
	"errors"
	"math"
	"testing"
)

func estimatePolicy(scope ChargePolicyScope) ChargePolicy {
	return ChargePolicy{
		Ref:                    VersionRef{ID: "policy", Version: "v1"},
		PricingRef:             VersionRef{ID: "pricing", Version: "v7"},
		Scope:                  scope,
		IncludeInputTokens:     true,
		IncludeOutputTokens:    true,
		IncludeFixedCharges:    true,
		IncludeResourceCharges: true,
	}
}

func estimateRouteFixture(id string, input, output int64) ChargeRoute {
	return ChargeRoute{ID: id, Pricing: PricingSnapshot{
		Ref: VersionRef{ID: "pricing", Version: "v7"}, Currency: "USD",
		InputPerMillionNano: input, OutputPerMillionNano: output,
		InputRatePresent: true, OutputRatePresent: true,
	}, ModelMaxOutputTokens: 100, ModelMaxOutputTokensPresent: true}
}

func TestEstimateMaxCustomerChargeIncludesPricingSnapshotFixedAndResourceCharges(t *testing.T) {
	t.Parallel()
	route := estimateRouteFixture("model-a", 0, 0)
	route.Pricing.FixedCharges = []ChargeComponent{{Name: "pricing-fixed", Amount: Money{Nano: 11, Currency: "USD"}}}
	route.Pricing.ResourceCharges = []ChargeComponent{{Name: "pricing-resource", Amount: Money{Nano: 4, Currency: "USD"}}}
	route.FixedCharges = []ChargeComponent{{Name: "route-fixed", Amount: Money{Nano: 2, Currency: "USD"}}}
	bound, err := EstimateMaxCustomerCharge(MaxChargeInput{
		Currency: "USD", InputTokens: 0, InputTokensPresent: true,
		Policy: ChargePolicy{
			Ref: VersionRef{ID: "policy", Version: "v1"}, PricingRef: VersionRef{ID: "pricing", Version: "v7"},
			Scope: ChargeSurfacedTurn, IncludeFixedCharges: true, IncludeResourceCharges: true,
		},
		Routes: []ChargeRoute{route},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bound.Amount.Nano != 17 {
		t.Fatalf("bound = %d, want pricing(11+4)+route(2)=17", bound.Amount.Nano)
	}
}

func TestEstimateMaxCustomerChargeUsesPessimisticOutputAndFixedComponents(t *testing.T) {
	t.Parallel()
	clientMax := int64(40)
	route := estimateRouteFixture("model-a", 10, 20)
	route.ClientMaxOutputTokens = &clientMax
	route.FixedCharges = []ChargeComponent{{Name: "request", Amount: Money{Nano: 7, Currency: "USD"}}}
	route.ResourceCharges = []ChargeComponent{{Name: "context", Amount: Money{Nano: 3, Currency: "USD"}}}
	bound, err := EstimateMaxCustomerCharge(MaxChargeInput{
		Currency: "USD", InputTokens: 1_000_000, InputTokensPresent: true,
		Policy: estimatePolicy(ChargeSurfacedTurn), Routes: []ChargeRoute{route},
	})
	if err != nil {
		t.Fatal(err)
	}
	// input 10 + lower client output 40*20/1M (ceil = 1) + fixed 7 + resource 3.
	if bound.Amount != (Money{Nano: 21, Currency: "USD"}) {
		t.Fatalf("bound = %+v, want 21 USD", bound.Amount)
	}
	if bound.PricingRef != (VersionRef{ID: "pricing", Version: "v7"}) || bound.ChargePolicyRef != estimatePolicy(ChargeSurfacedTurn).Ref {
		t.Fatalf("snapshot refs not bound: %+v", bound)
	}
}

func TestEstimateMaxCustomerChargeUsesWorstRouteOrAllPotentialLegs(t *testing.T) {
	t.Parallel()
	a := estimateRouteFixture("a", 10, 10)
	b := estimateRouteFixture("b", 10, 10)
	b.ModelMaxOutputTokens = 1_000_000
	b.FixedCharges = []ChargeComponent{{Name: "route-extra", Amount: Money{Nano: 5, Currency: "USD"}}}
	// Identical snapshot rates: input 1 token ceilings to 1 nano.
	// a output bound 100 tokens ceilings to 1 nano (total 2).
	// b output bound 1e6 tokens is 10 nanos plus route-level extra 5 (total 16).
	for _, tc := range []struct {
		name  string
		scope ChargePolicyScope
		want  int64
	}{
		{"surfaced chooses worst alternative", ChargeSurfacedTurn, 16},
		{"all potential legs sums", ChargeAllPotentialLegs, 18},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bound, err := EstimateMaxCustomerCharge(MaxChargeInput{
				Currency: "USD", InputTokens: 1, InputTokensPresent: true,
				Policy: estimatePolicy(tc.scope), Routes: []ChargeRoute{a, b},
			})
			if err != nil {
				t.Fatal(err)
			}
			if bound.Amount.Nano != tc.want {
				t.Fatalf("bound = %d, want %d", bound.Amount.Nano, tc.want)
			}
		})
	}
}

func TestEstimateMaxCustomerChargeBoundsHeterogeneousRouteCards(t *testing.T) {
	t.Parallel()
	cheap := estimateRouteFixture("cheap", 10, 10)
	expensive := estimateRouteFixture("expensive", 100, 100)
	expensive.ModelMaxOutputTokens = 1_000_000
	for _, tc := range []struct {
		name  string
		scope ChargePolicyScope
		want  int64
	}{
		{"surfaced uses the more expensive candidate card", ChargeSurfacedTurn, 200},
		{"all potential legs sums each candidate card", ChargeAllPotentialLegs, 211},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bound, err := EstimateMaxCustomerCharge(MaxChargeInput{
				Currency: "USD", InputTokens: 1_000_000, InputTokensPresent: true,
				Policy: estimatePolicy(tc.scope), Routes: []ChargeRoute{cheap, expensive},
			})
			if err != nil {
				t.Fatal(err)
			}
			if bound.Amount.Nano != tc.want {
				t.Fatalf("bound = %d, want %d", bound.Amount.Nano, tc.want)
			}
			if bound.PricingRef != estimatePolicy(tc.scope).PricingRef {
				t.Fatalf("pricing ref = %+v", bound.PricingRef)
			}
		})
	}
}

func TestEstimateMaxCustomerChargeAllowsMissingInputWhenNotInPolicy(t *testing.T) {
	t.Parallel()
	route := estimateRouteFixture("a", 1, 1)
	route.FixedCharges = []ChargeComponent{{Name: "request", Amount: Money{Nano: 9, Currency: "USD"}}}
	bound, err := EstimateMaxCustomerCharge(MaxChargeInput{
		Currency: "USD",
		Policy: ChargePolicy{
			Ref: VersionRef{ID: "policy", Version: "v1"}, PricingRef: VersionRef{ID: "pricing", Version: "v7"},
			Scope: ChargeSurfacedTurn, IncludeFixedCharges: true,
		},
		Routes: []ChargeRoute{route},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bound.Amount.Nano != 9 {
		t.Fatalf("bound = %d, want 9", bound.Amount.Nano)
	}
}

func TestEstimateMaxCustomerChargeFailsClosedForUnknownAndOverflow(t *testing.T) {
	t.Parallel()
	base := MaxChargeInput{Currency: "USD", Policy: estimatePolicy(ChargeSurfacedTurn), Routes: []ChargeRoute{estimateRouteFixture("a", 1, 1)}, Strict: true}
	if _, err := EstimateMaxCustomerCharge(base); !errors.Is(err, ErrEstimateUnbounded) {
		t.Fatalf("unknown exposure error = %v, want ErrEstimateUnbounded", err)
	}
	base.InputTokens, base.InputTokensPresent = 1, true
	base.InputTokens = math.MaxInt64
	base.Routes[0].Pricing.InputPerMillionNano = math.MaxInt64
	if _, err := EstimateMaxCustomerCharge(base); !errors.Is(err, ErrEstimateOverflow) {
		t.Fatalf("overflow error = %v, want ErrEstimateOverflow", err)
	}
}

func TestEstimateMaxCustomerChargeUsesExplicitCeilingForUnknown(t *testing.T) {
	t.Parallel()
	in := MaxChargeInput{
		Currency: "USD", Policy: estimatePolicy(ChargeSurfacedTurn), Routes: []ChargeRoute{estimateRouteFixture("a", 1, 1)},
		Strict: true, ConservativeCeiling: &Money{Nano: 99, Currency: "USD"},
	}
	bound, err := EstimateMaxCustomerCharge(in)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Amount.Nano != 99 || len(bound.Basis) != 1 || bound.Basis[0].Kind != "ceiling" {
		t.Fatalf("ceiling bound = %+v", bound)
	}
}

func TestEstimateMaxCustomerChargeUsesCeilingWhenModelMaxMissing(t *testing.T) {
	t.Parallel()
	route := estimateRouteFixture("unbounded", 1, 1)
	route.ModelMaxOutputTokensPresent = false
	route.ModelMaxOutputTokens = 0
	in := MaxChargeInput{
		Currency: "USD", InputTokens: 1, InputTokensPresent: true,
		Policy: estimatePolicy(ChargeSurfacedTurn), Routes: []ChargeRoute{route}, Strict: true,
	}
	if _, err := EstimateMaxCustomerCharge(in); !errors.Is(err, ErrEstimateUnbounded) {
		t.Fatalf("missing model max without ceiling = %v, want ErrEstimateUnbounded", err)
	}
	in.ConservativeCeiling = &Money{Nano: 77, Currency: "USD"}
	bound, err := EstimateMaxCustomerCharge(in)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Amount.Nano != 77 || len(bound.Basis) != 1 || bound.Basis[0].Kind != "ceiling" {
		t.Fatalf("ceiling bound = %+v", bound)
	}
}

func TestAdmissionCeilingTokenMathDiffersFromExactRatingFloor(t *testing.T) {
	t.Parallel()
	ceiling, err := tokensAtRate(1, 100)
	if err != nil {
		t.Fatal(err)
	}
	exact, err := exactTokensAtRate(1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if exact != 0 || ceiling != 1 {
		t.Fatalf("exact=%d ceiling=%d, want floor 0 and admission ceiling 1", exact, ceiling)
	}
}

func TestEstimateMaxCustomerChargeRejectsSnapshotAndCurrencyMismatch(t *testing.T) {
	t.Parallel()
	in := MaxChargeInput{Currency: "USD", InputTokensPresent: true, Policy: estimatePolicy(ChargeSurfacedTurn), Routes: []ChargeRoute{estimateRouteFixture("a", 1, 1)}}
	in.Routes[0].Pricing.Ref.Version = "other"
	if _, err := EstimateMaxCustomerCharge(in); !errors.Is(err, ErrEstimateSnapshot) {
		t.Fatalf("snapshot error = %v, want ErrEstimateSnapshot", err)
	}
	in.Routes[0].Pricing.Ref.Version = "v7"
	in.Routes[0].Pricing.Currency = "EUR"
	if _, err := EstimateMaxCustomerCharge(in); !errors.Is(err, ErrEstimateSnapshot) {
		t.Fatalf("wrapped currency error = %v, want ErrEstimateSnapshot", err)
	}
}
