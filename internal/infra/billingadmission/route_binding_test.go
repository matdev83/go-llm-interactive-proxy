package billingadmission_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingadmission"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 14.1 BLOCKER 1 RED: the quote exposes frozen per-route tariff bindings
// and admission carries them into the exposure transition.

type bindingCaptureStore struct {
	last billing.AdmitExposureInput
}

func (s *bindingCaptureStore) AdmitExposure(_ context.Context, in billing.AdmitExposureInput) (billing.CallExposure, error) {
	s.last = in
	return billing.CallExposure{AccountID: in.AccountID, CallID: in.CallID, Max: in.Max, PricingRef: in.PricingRef, ChargePolicyRef: in.ChargePolicyRef, RouteTariffs: in.RouteTariffs}, nil
}

func TestAdapterQuoteAndAdmitCarryRouteTariffBindings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	textKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}
	price, err := metering.ParseDecimal("0.000000002")
	if err != nil {
		t.Fatal(err)
	}
	modelTariff, err := economics.BuildTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "model-pricing", Version: "v1"}, RaterID: "reference"},
		"USD",
		[]economics.RatingRule{{ID: "text-input", Kind: economics.RatingRuleLinear, Component: &textKey, Currency: "USD", UnitPrice: &price}},
	)
	if err != nil {
		t.Fatal(err)
	}
	baseTariff := richAdapterTariff(t)
	bounds := richAdapterBounds(t)
	pricing := billing.PricingSnapshot{Ref: billing.VersionRef{ID: "retail-pricing", Version: "v3"}, Currency: "USD"}
	store := &bindingCaptureStore{}
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		ExposureStore: store, Currency: "USD",
		Identity: coreruntime.BillingIdentity{AccountID: func(context.Context, lipapi.Call) string { return "bound-routes" }},
		Policy: func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{
				Ref: billing.VersionRef{ID: "retail-policy", Version: "v10"}, PricingRef: pricing.Ref,
				Scope: billing.ChargeSurfacedTurn, IncludeInputTokens: true, IncludeFixedCharges: true,
				Retail: &billing.RetailSelectionPolicy{Mode: billing.RetailSelectionSurfacedWinner, Basis: billing.RetailBasisIndependent},
			}, nil
		},
		Pricing:        func(context.Context, string, string) (billing.PricingSnapshot, error) { return pricing, nil },
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 10, true, nil },
		BaseTariff:     func(context.Context, lipapi.Call) (economics.TariffSnapshot, error) { return baseTariff, nil },
		ModelTariff: func(_ context.Context, backend, _ string) (economics.TariffSnapshot, error) {
			if backend == "a" {
				return modelTariff, nil
			}
			return economics.TariffSnapshot{}, nil
		},
		ComponentBounds: func(context.Context, lipapi.Call) ([]billing.RichComponentBound, error) { return bounds, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	sel, err := routing.Parse("a:m1|b:m2")
	if err != nil {
		t.Fatal(err)
	}
	in := coreruntime.BillingAdmissionInput{
		Call: lipapi.Call{}, ALegID: "a-leg", Route: sel, AccountID: "bound-routes",
		RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 1},
	}
	bound, err := adapter.Quote(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(bound.RouteTariffs) != 2 {
		t.Fatalf("quote bindings = %+v, want one per route", bound.RouteTariffs)
	}
	byRoute := map[string]billing.RouteTariffBinding{}
	for _, binding := range bound.RouteTariffs {
		byRoute[binding.RouteID] = binding
	}
	if byRoute["a:m1"].TariffID != "model-pricing" || byRoute["a:m1"].TariffVersion != "v1" {
		t.Fatalf("override binding = %+v", byRoute["a:m1"])
	}
	if byRoute["b:m2"].TariffID != "retail-pricing" {
		t.Fatalf("base binding = %+v", byRoute["b:m2"])
	}
	exposure, err := adapter.Admit(ctx, coreruntime.BillingExposureAdmissionInput{BillingAdmissionInput: in, CallID: "call-bound-routes"})
	if err != nil {
		t.Fatal(err)
	}
	if len(exposure.RouteTariffs) != 2 || len(store.last.RouteTariffs) != 2 {
		t.Fatalf("admitted bindings = %+v store = %+v, want quote bindings carried", exposure.RouteTariffs, store.last.RouteTariffs)
	}
	for i := range bound.RouteTariffs {
		if exposure.RouteTariffs[i] != bound.RouteTariffs[i] || store.last.RouteTariffs[i] != bound.RouteTariffs[i] {
			t.Fatalf("binding drift: quote=%+v exposure=%+v store=%+v", bound.RouteTariffs, exposure.RouteTariffs, store.last.RouteTariffs)
		}
	}
}
