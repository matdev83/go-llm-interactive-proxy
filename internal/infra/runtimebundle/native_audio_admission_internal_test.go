package runtimebundle

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingadmission"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type nativeAudioExposureStore struct{}

func (nativeAudioExposureStore) AdmitExposure(context.Context, billing.AdmitExposureInput) (billing.CallExposure, error) {
	return billing.CallExposure{}, nil
}

func nativeAudioStockAdapter(t *testing.T) *billingadmission.Adapter {
	t.Helper()
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		ExposureStore: nativeAudioExposureStore{},
		Identity:      runtime.BillingIdentity{AccountID: func(context.Context, lipapi.Call) string { return "acct" }},
		Currency:      "USD",
		Policy: func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{
				Ref: billing.VersionRef{ID: "policy", Version: "v1"}, PricingRef: billing.VersionRef{ID: "pricing", Version: "v1"},
				Scope: billing.ChargeSurfacedTurn, IncludeInputTokens: true,
			}, nil
		},
		Pricing: func(context.Context, string, string) (billing.PricingSnapshot, error) {
			return billing.PricingSnapshot{Ref: billing.VersionRef{ID: "pricing", Version: "v1"}, Currency: "USD"}, nil
		},
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 1, true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestBackendKindInventoryIsPerGenerationCopy(t *testing.T) {
	t.Parallel()
	inventories := []modelregistry.BackendInventory{
		{BackendID: "native-inst", Kind: "openai-legacy"},
		{BackendID: "plain-inst", Kind: "anthropic"},
		{BackendID: "  ", Kind: "openai-responses"},
		{BackendID: "empty-kind", Kind: ""},
	}
	first := backendKindInventory(inventories)
	if len(first) != 3 {
		t.Fatalf("kinds = %+v, want 3 entries", first)
	}
	if first["native-inst"] != "openai-legacy" || first["plain-inst"] != "anthropic" || first["empty-kind"] != "" {
		t.Fatalf("kinds = %+v", first)
	}
	second := backendKindInventory(inventories)
	first["native-inst"] = "mutated"
	if second["native-inst"] != "openai-legacy" {
		t.Fatalf("per-generation maps share backing storage: second=%+v", second)
	}
	if backendKindInventory(nil) != nil {
		t.Fatal("empty inventory must yield nil kinds")
	}
}

func TestBindUnsupportedV2NativeUsageWiring(t *testing.T) {
	t.Parallel()
	stock := nativeAudioStockAdapter(t)
	stockPort := runtime.BillingExposureAdmission(stock)

	bound := bindUnsupportedV2NativeUsage(stock, map[string]string{"native-inst": "openai-legacy", "plain-inst": "anthropic"})
	if bound == stockPort {
		t.Fatal("an affected native kind must produce a bound clone, not the original adapter")
	}
	if _, ok := bound.(*billingadmission.Adapter); !ok {
		t.Fatalf("bound admission type = %T, want *billingadmission.Adapter", bound)
	}

	unaffected := bindUnsupportedV2NativeUsage(stock, map[string]string{"plain-inst": "anthropic"})
	if unaffected != stockPort {
		t.Fatal("no affected kinds must return the original adapter")
	}

	var external runtime.BillingExposureAdmission = gateStubExposure{}
	if got := bindUnsupportedV2NativeUsage(external, map[string]string{"native-inst": "openai-legacy"}); got != external {
		t.Fatal("an external binder implementation must be returned unchanged")
	}

	if bindUnsupportedV2NativeUsage(nil, map[string]string{"native-inst": "openai-legacy"}) != nil {
		t.Fatal("nil admission must stay nil")
	}
}
