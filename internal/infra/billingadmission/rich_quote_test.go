package billingadmission_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingadmission"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 14.1 RED: adapter quotes richer offers from immutable tariff semantics
// with finite enforceable bounds. Unknown duration/tool counts and missing
// evidence capabilities must deny or use a configured ceiling.

func richAdapterTariff(t *testing.T) economics.TariffSnapshot {
	t.Helper()
	mustDecimal := func(value string) *metering.Decimal {
		d, err := metering.ParseDecimal(value)
		if err != nil {
			t.Fatal(err)
		}
		return &d
	}
	textKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}
	toolKey := metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentToolQuery, Unit: metering.UnitCount, SchemaID: "retail.v1"}
	rules := []economics.RatingRule{
		{ID: "text-input", Kind: economics.RatingRuleLinear, Component: &textKey, Currency: "USD", UnitPrice: mustDecimal("0.000000001")},
		{ID: "call-fee", Kind: economics.RatingRuleFixed, Currency: "USD", FixedAmount: mustDecimal("0.000000005"), FixedScope: economics.FixedFeeScopeCall},
		{ID: "tool", Kind: economics.RatingRuleLinear, Component: &toolKey, Currency: "USD", UnitPrice: mustDecimal("0.000000002")},
	}
	tariff, err := economics.BuildTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "retail-pricing", Version: "v3"}, RaterID: "reference"},
		"USD", rules,
	)
	if err != nil {
		t.Fatal(err)
	}
	return tariff
}

func richAdapterBounds(t *testing.T) []billing.RichComponentBound {
	t.Helper()
	mustUpper := func(value string) metering.Decimal {
		d, err := metering.ParseDecimal(value)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	return []billing.RichComponentBound{
		{Key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}, Upper: mustUpper("100"), Enforceable: true},
		{Key: metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentToolQuery, Unit: metering.UnitCount, SchemaID: "retail.v1"}, Upper: mustUpper("3"), Enforceable: true},
	}
}

func richAdapterConfig(t *testing.T, tariff economics.TariffSnapshot, bounds []billing.RichComponentBound) billingadmission.Config {
	t.Helper()
	pricing := billing.PricingSnapshot{
		Ref: billing.VersionRef{ID: "retail-pricing", Version: "v3"}, Currency: "USD",
	}
	return billingadmission.Config{
		ExposureStore: exposureStore{}, Currency: "USD",
		Identity: coreruntime.BillingIdentity{AccountID: func(context.Context, lipapi.Call) string { return "rich-quote" }},
		Policy: func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{
				Ref: billing.VersionRef{ID: "retail-policy", Version: "v10"}, PricingRef: pricing.Ref,
				Scope: billing.ChargeSurfacedTurn, IncludeInputTokens: true, IncludeFixedCharges: true,
				Retail: &billing.RetailSelectionPolicy{Mode: billing.RetailSelectionSurfacedWinner, Basis: billing.RetailBasisIndependent},
			}, nil
		},
		Pricing:        func(context.Context, string, string) (billing.PricingSnapshot, error) { return pricing, nil },
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 10, true, nil },
		BaseTariff:     func(context.Context, lipapi.Call) (economics.TariffSnapshot, error) { return tariff, nil },
		ComponentBounds: func(context.Context, lipapi.Call) ([]billing.RichComponentBound, error) {
			return append([]billing.RichComponentBound(nil), bounds...), nil
		},
	}
}

func TestAdapterRichQuoteCoversNonTokenWithFiniteBounds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tariff := richAdapterTariff(t)
	adapter, err := billingadmission.NewAdapter(richAdapterConfig(t, tariff, richAdapterBounds(t)))
	if err != nil {
		t.Fatal(err)
	}
	primary := routing.Primary{Backend: "backend", Model: "model"}
	bound, err := adapter.Quote(ctx, coreruntime.BillingAdmissionInput{
		Call: lipapi.Call{}, ALegID: "a-leg", Route: &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}},
		RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 100 text * 1 + 5 call fee + 3 tool * 2 = 111 nano.
	if bound.Amount.Nano != 111 || bound.Amount.Currency != "USD" {
		t.Fatalf("rich bound = %+v, want 111 USD nano", bound.Amount)
	}
	if bound.PricingRef.ID != "retail-pricing" || bound.ChargePolicyRef.ID != "retail-policy" {
		t.Fatalf("rich refs = %+v/%+v", bound.PricingRef, bound.ChargePolicyRef)
	}
}

func TestAdapterRichQuoteDeniesUnknownDurationOrMissingCapability(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tariff := richAdapterTariff(t)
	// Unknown tool count: omit the tool bound.
	textOnly := richAdapterBounds(t)[:1]
	adapter, err := billingadmission.NewAdapter(richAdapterConfig(t, tariff, textOnly))
	if err != nil {
		t.Fatal(err)
	}
	primary := routing.Primary{Backend: "backend", Model: "model"}
	in := coreruntime.BillingAdmissionInput{
		Call: lipapi.Call{}, ALegID: "a-leg", Route: &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}},
		RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 1},
	}
	if _, err := adapter.Quote(ctx, in); !errors.Is(err, billing.ErrEstimateUnbounded) {
		t.Fatalf("unknown tool error = %v, want ErrEstimateUnbounded", err)
	}
	// Missing evidence capability: bounds are finite but the route cannot prove tool capture.
	cfg := richAdapterConfig(t, tariff, richAdapterBounds(t))
	cfg.RequiredCapabilities = []string{"tool_count"}
	cfg.RouteCapabilities = func(context.Context, string, string) ([]string, error) { return []string{"text"}, nil }
	adapter, err = billingadmission.NewAdapter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Quote(ctx, in); !errors.Is(err, billing.ErrEstimateUnbounded) {
		t.Fatalf("missing capability error = %v, want ErrEstimateUnbounded", err)
	}
	// BLOCKER 2: a bare configured number proves no contractual liability or
	// enforceable work bound, so even Strict with a ceiling stays denied.
	cfg.Strict = true
	ceiling := billing.Money{Nano: 555, Currency: "USD"}
	cfg.ConservativeCeiling = &ceiling
	adapter, err = billingadmission.NewAdapter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Quote(ctx, in); !errors.Is(err, billing.ErrEstimateUnbounded) {
		t.Fatalf("missing capability with bare ceiling = %v, want ErrEstimateUnbounded", err)
	}
	if _, err := adapter.Admit(ctx, coreruntime.BillingExposureAdmissionInput{BillingAdmissionInput: in, CallID: "call-bare-ceiling-deny"}); !errors.Is(err, billing.ErrEstimateUnbounded) {
		t.Fatalf("bare-ceiling admit = %v, want ErrEstimateUnbounded", err)
	}
}
