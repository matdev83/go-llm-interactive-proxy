package billingadmission_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingadmission"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Task 14.3 certification: a strict customer offer is rejected before any
// upstream spend or exposure write when ALL selected routes lack a required
// economic evidence capability. Unrelated scalar offers stay unsuppressed.

type countingExposureStore struct {
	admits atomic.Int32
}

func (s *countingExposureStore) AdmitExposure(_ context.Context, in billing.AdmitExposureInput) (billing.CallExposure, error) {
	s.admits.Add(1)
	return billing.CallExposure{AccountID: in.AccountID, CallID: in.CallID, Max: in.Max, PricingRef: in.PricingRef, ChargePolicyRef: in.ChargePolicyRef}, nil
}

func TestAdapterStrictQuoteRejectsWhenAllRoutesLackCapabilityWithoutSideEffects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tariff := richAdapterTariff(t)
	store := &countingExposureStore{}
	cfg := richAdapterConfig(t, tariff, richAdapterBounds(t))
	cfg.ExposureStore = store
	cfg.RequiredCapabilities = []string{"tool_count"}
	cfg.RouteCapabilities = func(context.Context, string, string) ([]string, error) { return nil, nil }
	adapter, err := billingadmission.NewAdapter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sel, err := routing.Parse("backend:m1|backend:m2")
	if err != nil {
		t.Fatal(err)
	}
	in := coreruntime.BillingAdmissionInput{
		Call: lipapi.Call{}, ALegID: "a-leg", Route: sel,
		RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 1},
	}
	if _, err := adapter.Quote(ctx, in); !errors.Is(err, billing.ErrEstimateUnbounded) {
		t.Fatalf("all-routes-lack capability quote = %v, want ErrEstimateUnbounded", err)
	}
	if _, err := adapter.Admit(ctx, coreruntime.BillingExposureAdmissionInput{BillingAdmissionInput: in, CallID: "call-strict-deny"}); !errors.Is(err, billing.ErrEstimateUnbounded) {
		t.Fatalf("all-routes-lack capability admit = %v, want ErrEstimateUnbounded", err)
	}
	if store.admits.Load() != 0 {
		t.Fatalf("denied strict admission wrote %d exposures, want 0 (reject before spend)", store.admits.Load())
	}
}

func TestAdapterLegacyQuoteIgnoresRichCapabilityConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pricing := billing.PricingSnapshot{
		Ref: billing.VersionRef{ID: "pricing", Version: "v1"}, Currency: "USD",
		InputPerMillionNano: 1_000_000, OutputPerMillionNano: 2_000_000,
		InputRatePresent: true, OutputRatePresent: true,
	}
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		ExposureStore: exposureStore{}, Currency: "USD",
		Identity: coreruntime.BillingIdentity{AccountID: func(context.Context, lipapi.Call) string { return "legacy-unaffected" }},
		Policy: func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{Ref: billing.VersionRef{ID: "policy", Version: "v1"}, PricingRef: pricing.Ref, Scope: billing.ChargeSurfacedTurn, IncludeInputTokens: true, IncludeOutputTokens: true}, nil
		},
		Pricing:        func(context.Context, string, string) (billing.PricingSnapshot, error) { return pricing, nil },
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 10, true, nil },
		// No BaseTariff: the scalar path owns this offer. Rich capability
		// machinery must not suppress unrelated offers.
		RequiredCapabilities: []string{"tool_count"},
		RouteCapabilities:    func(context.Context, string, string) ([]string, error) { return nil, nil },
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
		t.Fatalf("legacy quote with rich capability config = %v, want success", err)
	}
	if bound.Amount.Currency != "USD" || bound.Amount.Nano <= 0 {
		t.Fatalf("legacy bound = %+v, want positive USD bound", bound.Amount)
	}
}
