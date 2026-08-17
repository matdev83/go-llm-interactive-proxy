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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type exposureAdmissionFunc func(context.Context, runtime.BillingExposureAdmissionInput) (billing.CallExposure, error)

func (f exposureAdmissionFunc) Admit(ctx context.Context, in runtime.BillingExposureAdmissionInput) (billing.CallExposure, error) {
	return f(ctx, in)
}

func TestExecutorAuthoritativeExposureAdmissionUsesCallIDWithoutHold(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var calls, opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingIdentity.AccountID = func(context.Context, lipapi.Call) string { return "acct-exposure" }
	ex.BillingIdentity.CustomerPricingRef = func(context.Context, lipapi.Call) billing.VersionRef {
		return billing.VersionRef{ID: "pricing", Version: "v1"}
	}
	ex.BillingIdentity.ChargePolicyRef = func(context.Context, lipapi.Call) billing.VersionRef {
		return billing.VersionRef{ID: "policy", Version: "v1"}
	}
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in runtime.BillingExposureAdmissionInput) (billing.CallExposure, error) {
		calls.Add(1)
		if in.CallID == "" || len(in.CallID) < 3 {
			t.Errorf("missing BillingCallID: %q", in.CallID)
		}
		if in.Route == nil {
			t.Fatal("exposure admission did not receive route plan")
		}
		return billing.CallExposure{AccountID: "acct-exposure", CallID: in.CallID, Max: billing.Money{Nano: 1, Currency: "USD"}, PricingRef: billing.VersionRef{ID: "pricing", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"}, Status: billing.ExposureOpen}, nil
	})
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventMessageStarted}, {Kind: lipapi.EventResponseFinished}}), nil
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &lipapi.Call{Route: lipapi.RouteIntent{Selector: "backend:model"}, Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || opens.Load() != 1 {
		t.Fatalf("exposure admissions=%d backend opens=%d", calls.Load(), opens.Load())
	}
}

func TestExecutorAuthoritativeExposureAdmissionDenialDoesNotOpenProvider(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingIdentity.AccountID = func(context.Context, lipapi.Call) string { return "acct-exposure" }
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(context.Context, runtime.BillingExposureAdmissionInput) (billing.CallExposure, error) {
		return billing.CallExposure{}, errors.New("insufficient exposure")
	})
	ex.Backends = map[string]execbackend.Backend{"backend": {Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming), Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		opens.Add(1)
		return nil, nil
	}}}
	_, err = ex.Execute(context.Background(), &lipapi.Call{Route: lipapi.RouteIntent{Selector: "backend:model"}, Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}}})
	if !errors.Is(err, runtime.ErrBillingAdmissionDenied) {
		t.Fatalf("error = %v, want ErrBillingAdmissionDenied", err)
	}
	if opens.Load() != 0 {
		t.Fatalf("provider opens = %d, want 0", opens.Load())
	}
}

func TestExecutorBillingAdmissionUsesTokenEstimateWithoutRouteSizeConstraints(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingIdentity.AccountID = func(context.Context, lipapi.Call) string { return "acct-estimate" }
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.RequestTokenEstimator = fixedRequestTokenEstimator{available: true, tokens: 42}
	var sawSize routing.RequestSizeEstimate
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in runtime.BillingExposureAdmissionInput) (billing.CallExposure, error) {
		sawSize = in.RequestSize
		return billing.CallExposure{
			AccountID: "acct-estimate", CallID: in.CallID, Max: billing.Money{Nano: 1, Currency: "USD"},
			PricingRef: billing.VersionRef{ID: "pricing", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
			Status: billing.ExposureOpen,
		}, nil
	})
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventMessageStarted}, {Kind: lipapi.EventResponseFinished}}), nil
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "backend:model"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if !sawSize.Available || sawSize.Tokens != 42 {
		t.Fatalf("billing RequestSize = %+v, want available tokens=42 without route size constraints", sawSize)
	}
}
