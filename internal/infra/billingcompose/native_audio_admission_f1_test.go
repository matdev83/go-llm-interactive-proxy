package billingcompose_test

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
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// f1V2AdmissionStore is a version-aware exposure store that reports a
// v2_active durable marker, so Admit auto-selects V2 exactly as production
// does after legal cutover. It counts every exposure write so a rejected
// scalar offer can be proven side-effect free.
type f1V2AdmissionStore struct {
	admits atomic.Int32
}

func (s *f1V2AdmissionStore) AdmitExposure(_ context.Context, in billing.AdmitExposureInput) (billing.CallExposure, error) {
	s.admits.Add(1)
	return f1AdmittedExposure(in), nil
}

func (s *f1V2AdmissionStore) AdmitExposureWithOwner(_ context.Context, in billing.AdmitExposureInput, _ string) (billing.CallExposure, error) {
	s.admits.Add(1)
	return f1AdmittedExposure(in), nil
}

func (s *f1V2AdmissionStore) GetAccountingCutover(context.Context) (billing.AccountingCutoverMarker, error) {
	return billing.AccountingCutoverMarker{State: billing.AccountingCutoverV2Active}, nil
}

func f1AdmittedExposure(in billing.AdmitExposureInput) billing.CallExposure {
	return billing.CallExposure{
		AccountID: in.AccountID, CallID: in.CallID, Max: in.Max,
		PricingRef: in.PricingRef, ChargePolicyRef: in.ChargePolicyRef, RouteTariffs: in.RouteTariffs,
	}
}

func f1AdmissionInput() coreruntime.BillingExposureAdmissionInput {
	primary := routing.Primary{Backend: f1BackendID, Model: f1ModelID}
	return coreruntime.BillingExposureAdmissionInput{
		BillingAdmissionInput: coreruntime.BillingAdmissionInput{
			ALegID:      f1ALegID,
			Route:       &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}},
			RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: f1InputTokens},
		},
		CallID: "bc_00000000000000000000000000000001",
	}
}

func f1StockAdapter(t *testing.T, store billing.ExposureAdmissionStore) *billingadmission.Adapter {
	t.Helper()
	catalog, _, _, _ := seedCatalog(t)
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		ExposureStore: store,
		Identity: coreruntime.BillingIdentity{AccountID: func(context.Context, lipapi.Call) string {
			return f1AccountID
		}},
		Currency:       "USD",
		Policy:         catalog.Policy,
		Pricing:        catalog.RoutePricing,
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 100, true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func f1HasPositiveNativeAudio(obs sdkmetering.Observation) bool {
	for _, measure := range obs.Measures {
		if measure.Key.Component == sdkmetering.ComponentAudioToken && measure.Value != nil && measure.Value.Coefficient != "0" {
			return true
		}
	}
	return false
}

// TestF1OpenAINativeAudioIsSafeOrRejectedBeforeSpend is the F1 pre-spend vector.
// The real OpenAI native wire mapper emits present positive audio, which the
// scalar V2 rating path cannot prove and therefore leaves incomplete at
// post-usage. The production fix is a pre-execution rejection: a candidate
// adapter bound to the affected backend instance (by trusted configured factory
// kind) must refuse every V2 offer before Quote or any exposure write, so a
// route with unpriced native audio never spends. V1 admission is deliberately
// unchanged.
func TestF1OpenAINativeAudioIsSafeOrRejectedBeforeSpend(t *testing.T) {
	t.Parallel()

	// Source evidence: the native mapper really surfaces present positive audio
	// and the scalar V2 rating path stays fail-closed for it.
	catalog, pricing, policy, _ := seedCatalog(t)
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	obs := f1OpenAIWireObservation(t, callID, `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"audio_tokens":3},"completion_tokens_details":{"audio_tokens":4}}`)
	if !f1HasPositiveNativeAudio(obs) {
		t.Fatalf("native mapper must surface present positive audio: %+v", obs.Measures)
	}
	if _, err := f1RateThroughResolver(t, catalog, callID, pricing.Ref, policy.Ref, obs); err == nil {
		t.Fatalf("unpriced positive native audio must stay incomplete at post-usage")
	}

	store := &f1V2AdmissionStore{}
	adapter := f1StockAdapter(t, store)

	// Control: the unbound stock adapter admits the same route under V2 (the
	// durable marker is v2_active), so rejection below is caused by the binding.
	if _, err := adapter.AdmitV2(context.Background(), f1AdmissionInput()); err != nil {
		t.Fatalf("unbound control V2 admission failed: %v", err)
	}
	if got := store.admits.Load(); got != 1 {
		t.Fatalf("unbound control wrote %d exposures, want 1", got)
	}

	// Bound candidate: the backend is flagged by trusted factory kind, so both
	// the explicit V2 entrypoint and the auto-V2 Admit path reject before spend.
	bound := adapter.BindUnsupportedV2NativeUsageBackends([]string{f1BackendID})
	before := store.admits.Load()
	if _, err := bound.AdmitV2(context.Background(), f1AdmissionInput()); !errors.Is(err, billing.ErrEstimateInvalid) {
		t.Fatalf("bound AdmitV2 err = %v, want ErrEstimateInvalid", err)
	}
	if _, err := bound.Admit(context.Background(), f1AdmissionInput()); !errors.Is(err, billing.ErrEstimateInvalid) {
		t.Fatalf("bound auto-V2 Admit err = %v, want ErrEstimateInvalid", err)
	}
	if got := store.admits.Load(); got != before {
		t.Fatalf("bound V2 rejection wrote %d new exposures, want 0", got-before)
	}

	// V1 is untouched: the same bound adapter admits under the legacy owner.
	if _, err := bound.AdmitWithOwner(context.Background(), f1AdmissionInput(), billing.PostingOwnerV1); err != nil {
		t.Fatalf("V1 admission must be unchanged: %v", err)
	}
	if got := store.admits.Load(); got != before+1 {
		t.Fatalf("V1 admission wrote %d exposures, want 1", got-before)
	}
}
