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

type creditGateFunc func(context.Context, string) error

func (f creditGateFunc) Check(ctx context.Context, accountID string) error { return f(ctx, accountID) }

func TestExecutorCheapCreditScreenDeniesBeforeRoutePlanning(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	var calls atomic.Int32
	ex.BillingIdentity.AccountID = func(context.Context, lipapi.Call) string { return "acct-screen" }
	ex.BillingCreditGate = creditGateFunc(func(_ context.Context, accountID string) error {
		calls.Add(1)
		if accountID != "acct-screen" {
			t.Fatalf("account id = %q", accountID)
		}
		return billing.ErrCreditScreenDenied
	})
	_, err = ex.Execute(context.Background(), &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "backend:model"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if !errors.Is(err, runtime.ErrBillingCreditScreenDenied) {
		t.Fatalf("error = %v, want ErrBillingCreditScreenDenied", err)
	}
	if errors.Is(err, runtime.ErrBillingCreditScreenUnavailable) {
		t.Fatalf("denied credit must not also classify as unavailable: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("credit-screen calls = %d, want 1", calls.Load())
	}
}

func TestExecutorCheapCreditUnavailableDoesNotClassifyAsDenied(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingIdentity.AccountID = func(context.Context, lipapi.Call) string { return "acct-screen" }
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error {
		return billing.ErrCreditScreenUnavailable
	})
	_, err = ex.Execute(context.Background(), &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "backend:model"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if !errors.Is(err, runtime.ErrBillingAdmissionDenied) {
		t.Fatalf("error = %v, want ErrBillingAdmissionDenied", err)
	}
	if !errors.Is(err, runtime.ErrBillingCreditScreenUnavailable) {
		t.Fatalf("error = %v, want ErrBillingCreditScreenUnavailable", err)
	}
	if errors.Is(err, runtime.ErrBillingCreditScreenDenied) {
		t.Fatalf("store outage must not classify as credit denied: %v", err)
	}
}

func TestExecutorAuthoritativeWithoutCreditGateDeniesBeforeProviderOpen(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingAuthoritative = true
	ex.BillingIdentity.AccountID = func(context.Context, lipapi.Call) string { return "acct-authoritative" }
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(context.Context, runtime.BillingExposureAdmissionInput) (billing.CallExposure, error) {
		t.Fatal("exposure admission must not run when cheap credit gate is missing")
		return billing.CallExposure{}, nil
	})
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
		Route:    lipapi.RouteIntent{Selector: "backend:model"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if !errors.Is(err, runtime.ErrBillingAdmissionDenied) || opens.Load() != 0 {
		t.Fatalf("error=%v provider_opens=%d", err, opens.Load())
	}
}

func TestExecutorAuthoritativeExposureIsRequiredBeforeProviderOpen(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingAuthoritative = true
	ex.BillingIdentity.AccountID = func(context.Context, lipapi.Call) string { return "acct-authoritative" }
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
		Route:    lipapi.RouteIntent{Selector: "backend:model"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if !errors.Is(err, runtime.ErrBillingAdmissionDenied) || opens.Load() != 0 {
		t.Fatalf("error=%v provider_opens=%d", err, opens.Load())
	}
}

func TestExecutorExposureAdmissionDenialDoesNotOpenProvider(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingAuthoritative = true
	ex.BillingIdentity.AccountID = func(context.Context, lipapi.Call) string { return "acct-exposure" }
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(context.Context, runtime.BillingExposureAdmissionInput) (billing.CallExposure, error) {
		return billing.CallExposure{}, errors.New("insufficient exposure")
	})
	ex.Backends = map[string]execbackend.Backend{"backend": {
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			opens.Add(1)
			return nil, nil
		},
	}}
	_, err = ex.Execute(context.Background(), &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "backend:model"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if !errors.Is(err, runtime.ErrBillingAdmissionDenied) || opens.Load() != 0 {
		t.Fatalf("error=%v provider_opens=%d", err, opens.Load())
	}
}
