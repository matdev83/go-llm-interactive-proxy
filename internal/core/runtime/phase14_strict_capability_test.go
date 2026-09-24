package runtime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingadmission"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 14.3 certification: a strict customer offer whose required evidence
// capability is missing on ALL selected routes is denied through the genuine
// quote path before any provider open or exposure write. Traffic without a
// monetary binding keeps flowing (observation-only, certified by the existing
// stock-runtime tests).

type phase14CountingExposureStore struct {
	admits atomic.Int32
}

func (s *phase14CountingExposureStore) AdmitExposure(_ context.Context, in billing.AdmitExposureInput) (billing.CallExposure, error) {
	s.admits.Add(1)
	return billing.CallExposure{AccountID: in.AccountID, CallID: in.CallID, Max: in.Max, PricingRef: in.PricingRef, ChargePolicyRef: in.ChargePolicyRef}, nil
}

func phase14StrictTariff(t *testing.T) economics.TariffSnapshot {
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
	tariff, err := economics.BuildTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "retail-pricing", Version: "v3"}, RaterID: "reference"},
		"USD",
		[]economics.RatingRule{
			{ID: "text-input", Kind: economics.RatingRuleLinear, Component: &textKey, Currency: "USD", UnitPrice: mustDecimal("0.000000001")},
			{ID: "tool", Kind: economics.RatingRuleLinear, Component: &toolKey, Currency: "USD", UnitPrice: mustDecimal("0.000000002")},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return tariff
}

func TestExecutorStrictCapabilityDenialOpensNoProviderAndWritesNoExposure(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	tariff := phase14StrictTariff(t)
	pricing := billing.PricingSnapshot{Ref: billing.VersionRef{ID: "retail-pricing", Version: "v3"}, Currency: "USD"}
	policy := billing.ChargePolicy{
		Ref: billing.VersionRef{ID: "retail-policy", Version: "v10"}, PricingRef: pricing.Ref,
		Scope: billing.ChargeSurfacedTurn, IncludeInputTokens: true, IncludeFixedCharges: true,
		Retail: &billing.RetailSelectionPolicy{Mode: billing.RetailSelectionSurfacedWinner, Basis: billing.RetailBasisIndependent},
	}
	mustUpper := func(value string) metering.Decimal {
		d, err := metering.ParseDecimal(value)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	bounds := []billing.RichComponentBound{
		{Key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "retail.v1"}, Upper: mustUpper("100"), Enforceable: true},
		{Key: metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentToolQuery, Unit: metering.UnitCount, SchemaID: "retail.v1"}, Upper: mustUpper("3"), Enforceable: true},
	}
	store := &phase14CountingExposureStore{}
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		ExposureStore: store, Currency: "USD",
		Identity: runtime.BillingIdentity{AccountID: func(context.Context, lipapi.Call) string { return "acct-strict" }},
		Policy:   func(context.Context, lipapi.Call) (billing.ChargePolicy, error) { return policy, nil },
		Pricing:  func(context.Context, string, string) (billing.PricingSnapshot, error) { return pricing, nil },
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) {
			return 10, true, nil
		},
		BaseTariff:      func(context.Context, lipapi.Call) (economics.TariffSnapshot, error) { return tariff, nil },
		ComponentBounds: func(context.Context, lipapi.Call) ([]billing.RichComponentBound, error) { return bounds, nil },
		// Both failover candidates lack the tool-count evidence capability.
		RequiredCapabilities: []string{"tool_count"},
		RouteCapabilities:    func(context.Context, string, string) ([]string, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingIdentity.AccountID = func(context.Context, lipapi.Call) string { return "acct-strict" }
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = adapter
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return nil, errors.New("must not open")
			},
		},
	}
	_, err = ex.Execute(context.Background(), &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "backend:m1|backend:m2"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if !errors.Is(err, runtime.ErrBillingAdmissionDenied) {
		t.Fatalf("error = %v, want ErrBillingAdmissionDenied", err)
	}
	if !errors.Is(err, billing.ErrEstimateUnbounded) {
		t.Fatalf("error = %v, want capability cause ErrEstimateUnbounded", err)
	}
	if opens.Load() != 0 {
		t.Fatalf("provider opens = %d, want 0 (deny before spend)", opens.Load())
	}
	if store.admits.Load() != 0 {
		t.Fatalf("exposure writes = %d, want 0 (deny before admission)", store.admits.Load())
	}
}
