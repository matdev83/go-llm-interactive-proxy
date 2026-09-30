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

const (
	guardNativeBackend = "arbitrary-native-instance"
	guardOtherBackend  = "arbitrary-other-instance"
	guardModel         = "model"
)

// guardVersionedStore reports a v2_active marker so the adapter auto-selects V2,
// and counts every exposure write (versioned or legacy) for side-effect proof.
type guardVersionedStore struct {
	admits atomic.Int32
}

func (s *guardVersionedStore) AdmitExposure(_ context.Context, in billing.AdmitExposureInput) (billing.CallExposure, error) {
	s.admits.Add(1)
	return guardExposure(in), nil
}

func (s *guardVersionedStore) AdmitExposureWithOwner(_ context.Context, in billing.AdmitExposureInput, _ string) (billing.CallExposure, error) {
	s.admits.Add(1)
	return guardExposure(in), nil
}

func (s *guardVersionedStore) GetAccountingCutover(context.Context) (billing.AccountingCutoverMarker, error) {
	return billing.AccountingCutoverMarker{State: billing.AccountingCutoverV2Active}, nil
}

// guardLegacyStore has no version-aware ports, so the adapter stays on V1.
type guardLegacyStore struct {
	admits atomic.Int32
}

func (s *guardLegacyStore) AdmitExposure(_ context.Context, in billing.AdmitExposureInput) (billing.CallExposure, error) {
	s.admits.Add(1)
	return guardExposure(in), nil
}

func guardExposure(in billing.AdmitExposureInput) billing.CallExposure {
	return billing.CallExposure{
		AccountID: in.AccountID, CallID: in.CallID, Max: in.Max,
		PricingRef: in.PricingRef, ChargePolicyRef: in.ChargePolicyRef, RouteTariffs: in.RouteTariffs,
	}
}

func guardBaseConfig(store billing.ExposureAdmissionStore) billingadmission.Config {
	pricing := billing.PricingSnapshot{
		Ref: billing.VersionRef{ID: "pricing-guard", Version: "v1"}, Currency: "USD",
		InputPerMillionNano: 1_000_000, OutputPerMillionNano: 2_000_000,
		InputRatePresent: true, OutputRatePresent: true,
	}
	return billingadmission.Config{
		ExposureStore: store, Currency: "USD",
		Identity: coreruntime.BillingIdentity{AccountID: func(context.Context, lipapi.Call) string { return "acct-guard" }},
		Policy: func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{
				Ref: billing.VersionRef{ID: "policy-guard", Version: "v1"}, PricingRef: pricing.Ref,
				Scope: billing.ChargeSurfacedTurn, IncludeInputTokens: true, IncludeOutputTokens: true,
			}, nil
		},
		Pricing:        func(context.Context, string, string) (billing.PricingSnapshot, error) { return pricing, nil },
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 10, true, nil },
	}
}

func guardRoute(t *testing.T, selector string) *routing.Selector {
	t.Helper()
	sel, err := routing.Parse(selector)
	if err != nil {
		t.Fatalf("parse route %q: %v", selector, err)
	}
	return sel
}

func guardInput(sel *routing.Selector) coreruntime.BillingExposureAdmissionInput {
	return coreruntime.BillingExposureAdmissionInput{
		BillingAdmissionInput: coreruntime.BillingAdmissionInput{
			ALegID: "a-guard", Route: sel,
			RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 10},
		},
		CallID: "bc_00000000000000000000000000000002",
	}
}

func TestAdapterV2GuardRejectsBoundNativeBackendBeforeExposure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &guardVersionedStore{}
	adapter, err := billingadmission.NewAdapter(guardBaseConfig(store))
	if err != nil {
		t.Fatal(err)
	}
	input := guardInput(guardRoute(t, guardNativeBackend+":"+guardModel))

	// Unbound control admits the flagged route under auto-V2.
	if _, err := adapter.Admit(ctx, input); err != nil {
		t.Fatalf("unbound control admission: %v", err)
	}
	if got := store.admits.Load(); got != 1 {
		t.Fatalf("unbound control wrote %d exposures, want 1", got)
	}

	bound := adapter.BindUnsupportedV2NativeUsageBackends([]string{guardNativeBackend})
	// Quote alone remains side-effect free and still computes the offer.
	if _, err := bound.Quote(ctx, input.BillingAdmissionInput); err != nil {
		t.Fatalf("Quote must not reject the offer: %v", err)
	}
	before := store.admits.Load()

	if _, err := bound.AdmitV2(ctx, input); !errors.Is(err, billing.ErrEstimateInvalid) {
		t.Fatalf("bound AdmitV2 err = %v, want ErrEstimateInvalid", err)
	}
	if _, err := bound.Admit(ctx, input); !errors.Is(err, billing.ErrEstimateInvalid) {
		t.Fatalf("bound auto-V2 Admit err = %v, want ErrEstimateInvalid", err)
	}
	if _, err := bound.AdmitWithOwner(ctx, input, billing.PostingOwnerV2); !errors.Is(err, billing.ErrEstimateInvalid) {
		t.Fatalf("bound explicit-owner V2 err = %v, want ErrEstimateInvalid", err)
	}
	if got := store.admits.Load(); got != before {
		t.Fatalf("bound V2 rejection wrote %d exposures, want 0", got-before)
	}
}

func TestAdapterV2GuardLeavesUnflaggedRoutesAndV1Unchanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &guardVersionedStore{}
	adapter, err := billingadmission.NewAdapter(guardBaseConfig(store))
	if err != nil {
		t.Fatal(err)
	}
	nativeIDs := []string{guardNativeBackend}
	bound := adapter.BindUnsupportedV2NativeUsageBackends(nativeIDs)

	// The bind copied the IDs: mutating the caller slice must not change it.
	nativeIDs[0] = "mutated-after-bind"

	// A route that does not touch the bound backend still admits under V2.
	if _, err := bound.Admit(ctx, guardInput(guardRoute(t, guardOtherBackend+":"+guardModel))); err != nil {
		t.Fatalf("unflagged route must still admit: %v", err)
	}
	// A mixed failover route that does touch the bound backend still rejects.
	if _, err := bound.Admit(ctx, guardInput(guardRoute(t, guardOtherBackend+":"+guardModel+"|"+guardNativeBackend+":"+guardModel))); !errors.Is(err, billing.ErrEstimateInvalid) {
		t.Fatalf("mixed route err = %v, want ErrEstimateInvalid", err)
	}
	// The receiver is untouched and remains usable for the bound backend.
	if _, err := adapter.Admit(ctx, guardInput(guardRoute(t, guardNativeBackend+":"+guardModel))); err != nil {
		t.Fatalf("receiver must remain unbound: %v", err)
	}
	if got := store.admits.Load(); got != 2 {
		t.Fatalf("writes = %d, want 2 (unflagged + receiver control)", got)
	}

	// V1 admission is unchanged even on a bound backend.
	legacy := &guardLegacyStore{}
	v1Adapter, err := billingadmission.NewAdapter(guardBaseConfig(legacy))
	if err != nil {
		t.Fatal(err)
	}
	v1Bound := v1Adapter.BindUnsupportedV2NativeUsageBackends([]string{guardNativeBackend})
	if _, err := v1Bound.Admit(ctx, guardInput(guardRoute(t, guardNativeBackend+":"+guardModel))); err != nil {
		t.Fatalf("V1 admission must be unchanged: %v", err)
	}
	if got := legacy.admits.Load(); got != 1 {
		t.Fatalf("V1 wrote %d exposures, want 1", got)
	}
}

func TestAdapterV2GuardRejectsRichTariffWithoutNativeCoverage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// This rich tariff carries only retail.v1 text/tool/fixed rules and no
	// OpenAI native audio coverage, so it cannot prove native compatibility.
	cfg := richAdapterConfig(t, richAdapterTariff(t), richAdapterBounds(t))
	store := &guardVersionedStore{}
	cfg.ExposureStore = store
	adapter, err := billingadmission.NewAdapter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bound := adapter.BindUnsupportedV2NativeUsageBackends([]string{guardNativeBackend})
	input := guardInput(guardRoute(t, guardNativeBackend+":"+guardModel))

	// Quote still computes the rich offer; the guard is admission-only.
	if _, err := bound.Quote(ctx, input.BillingAdmissionInput); err != nil {
		t.Fatalf("Quote must not reject the rich offer: %v", err)
	}
	before := store.admits.Load()
	if _, err := bound.AdmitV2(ctx, input); !errors.Is(err, billing.ErrEstimateInvalid) {
		t.Fatalf("incompatible rich-tariff AdmitV2 must fail closed, got %v", err)
	}
	if _, err := bound.Admit(ctx, input); !errors.Is(err, billing.ErrEstimateInvalid) {
		t.Fatalf("incompatible rich-tariff auto-V2 Admit must fail closed, got %v", err)
	}
	if got := store.admits.Load(); got != before {
		t.Fatalf("rich-incompatible rejection wrote %d exposures, want 0", got-before)
	}
}
