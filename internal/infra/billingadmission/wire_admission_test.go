package billingadmission_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingadmission"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type capturingExposureStore struct {
	lastInput billing.AdmitExposureInput
}

func (s *capturingExposureStore) AdmitExposure(_ context.Context, in billing.AdmitExposureInput) (billing.CallExposure, error) {
	s.lastInput = in
	return billing.CallExposure{
		AccountID:       in.AccountID,
		PricingRef:      in.PricingRef,
		ChargePolicyRef: in.ChargePolicyRef,
	}, nil
}

// TestWireAdmission_BoundedFactsQuoteAndAdmit proves that Adapter.Quote and Adapter.Admit
// correctly consume bounded wire facts (MaxOutputTokens, Scope, AccountID) when lipapi.Call is zero
// (Requirements 15.6, 19).
func TestWireAdmission_BoundedFactsQuoteAndAdmit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	pricing := billing.PricingSnapshot{
		Ref: billing.VersionRef{ID: "pricing-wire", Version: "v1"}, Currency: "USD",
		InputPerMillionNano: 1_000_000, OutputPerMillionNano: 2_000_000,
		InputRatePresent: true, OutputRatePresent: true,
	}

	store := &capturingExposureStore{}
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		ExposureStore: store,
		Currency:      "USD",
		Identity: coreruntime.BillingIdentity{
			WireBounded: true,
			AccountID: func(_ context.Context, _ lipapi.Call) string {
				return "" // returns empty on zero call
			},
		},
		Policy: func(_ context.Context, _ lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{
				Ref:                 billing.VersionRef{ID: "policy-wire", Version: "v1"},
				PricingRef:          pricing.Ref,
				Scope:               billing.ChargeSurfacedTurn,
				IncludeInputTokens:  true,
				IncludeOutputTokens: true,
			}, nil
		},
		Pricing: func(_ context.Context, _, _ string) (billing.PricingSnapshot, error) {
			return pricing, nil
		},
		// ModelMaxOutput returns 100, but wire client max output will constrain it to 50
		ModelMaxOutput: func(_ context.Context, _, _ string) (int64, bool, error) {
			return 100, true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	primary := routing.Primary{Backend: "backend-wire", Model: "model-wire"}
	sel := &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}}
	wireMaxOut := 50

	in := coreruntime.BillingExposureAdmissionInput{
		BillingAdmissionInput: coreruntime.BillingAdmissionInput{
			Call:            lipapi.Call{}, // zero Call on wire fast path
			TraceID:         "trace-wire-01",
			ALegID:          "aleg-wire-01",
			BillingCallID:   "call_01j00000000000000000000005",
			Route:           sel,
			RequestSize:     routing.RequestSizeEstimate{Available: true, Tokens: 10},
			MaxOutputTokens: &wireMaxOut,
			AccountID:       "acct-wire-bounded",
			Scope: scope.PrincipalScopeView{
				PrincipalID: scope.Known("prin-wire-01"),
			},
		},
		CallID: "call_01j00000000000000000000005",
	}

	// 1. Test Quote uses bounded MaxOutputTokens (50 instead of 100)
	bound, err := adapter.Quote(ctx, in.BillingAdmissionInput)
	if err != nil {
		t.Fatalf("Quote failed on wire input: %v", err)
	}
	// 10 input tokens * 1 nano = 10 nano.
	// 50 output tokens * 2 nano = 100 nano.
	// Total: 110 nano.
	if bound.Amount.Nano != 110 {
		t.Fatalf("bound.Amount.Nano = %d, want 110 (using bounded MaxOutputTokens=50)", bound.Amount.Nano)
	}

	// 2. Test Admit uses bounded AccountID
	exposure, err := adapter.Admit(ctx, in)
	if err != nil {
		t.Fatalf("Admit failed on wire input: %v", err)
	}
	if exposure.AccountID != "acct-wire-bounded" {
		t.Fatalf("exposure.AccountID = %q, want acct-wire-bounded", exposure.AccountID)
	}
	if store.lastInput.AccountID != "acct-wire-bounded" {
		t.Fatalf("store.lastInput.AccountID = %q, want acct-wire-bounded", store.lastInput.AccountID)
	}
	if store.lastInput.Max.Nano != 110 {
		t.Fatalf("store.lastInput.Max.Nano = %d, want 110", store.lastInput.Max.Nano)
	}
}
