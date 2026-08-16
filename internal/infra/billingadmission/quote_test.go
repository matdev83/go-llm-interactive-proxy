package billingadmission_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingadmission"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type exposureStore struct{}

func (exposureStore) AdmitExposure(context.Context, billing.AdmitExposureInput) (billing.CallExposure, error) {
	return billing.CallExposure{}, nil
}

func TestAdapterQuoteIsSideEffectFreeAndBindsPessimisticSnapshot(t *testing.T) {
	ctx := context.Background()
	pricing := billing.PricingSnapshot{
		Ref: billing.VersionRef{ID: "pricing", Version: "v1"}, Currency: "USD",
		InputPerMillionNano: 1_000_000, OutputPerMillionNano: 2_000_000,
		InputRatePresent: true, OutputRatePresent: true,
		FixedCharges: []billing.ChargeComponent{{Name: "request", Amount: billing.Money{Nano: 5, Currency: "USD"}}},
	}
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		ExposureStore: exposureStore{}, Currency: "USD",
		Identity: coreruntime.BillingIdentity{AccountID: func(context.Context, lipapi.Call) string { return "quote-only" }},
		Policy: func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{Ref: billing.VersionRef{ID: "policy", Version: "v1"}, PricingRef: pricing.Ref, Scope: billing.ChargeSurfacedTurn, IncludeInputTokens: true, IncludeOutputTokens: true, IncludeFixedCharges: true}, nil
		},
		Pricing:        func(context.Context, string, string) (billing.PricingSnapshot, error) { return pricing, nil },
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 10, true, nil },
	})
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
	if bound.Amount != (billing.Money{Nano: 26, Currency: "USD"}) {
		t.Fatalf("quote = %+v, want 26 USD", bound.Amount)
	}
	if bound.PricingRef != pricing.Ref || bound.ChargePolicyRef.ID != "policy" {
		t.Fatalf("quote refs = %+v/%+v", bound.PricingRef, bound.ChargePolicyRef)
	}
}

func TestAdapterQuoteSumsDuplicateParallelLeavesForAllPotentialLegs(t *testing.T) {
	ctx := context.Background()
	pricing := billing.PricingSnapshot{
		Ref: billing.VersionRef{ID: "pricing", Version: "v1"}, Currency: "USD",
		InputPerMillionNano: 0, OutputPerMillionNano: 1_000_000,
		InputRatePresent: true, OutputRatePresent: true,
	}
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		ExposureStore: exposureStore{}, Currency: "USD",
		Identity: coreruntime.BillingIdentity{AccountID: func(context.Context, lipapi.Call) string { return "quote-dup" }},
		Policy: func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{
				Ref: billing.VersionRef{ID: "policy", Version: "v1"}, PricingRef: pricing.Ref,
				Scope: billing.ChargeAllPotentialLegs, IncludeOutputTokens: true,
			}, nil
		},
		Pricing:        func(context.Context, string, string) (billing.PricingSnapshot, error) { return pricing, nil },
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 10, true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	sel, err := routing.Parse("a:m1!a:m1")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := adapter.Quote(ctx, coreruntime.BillingAdmissionInput{
		Call: lipapi.Call{}, ALegID: "a-leg", Route: sel,
		RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Each leg: 10 output tokens * 1 nano/token = 10; two parallel legs => 20.
	if bound.Amount != (billing.Money{Nano: 20, Currency: "USD"}) {
		t.Fatalf("quote = %+v, want 20 USD for two identical parallel legs", bound.Amount)
	}
}
