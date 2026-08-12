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

type billingAdmissionFunc func(context.Context, runtime.BillingAdmissionInput) (billing.Authorization, error)

func (f billingAdmissionFunc) Authorize(ctx context.Context, in runtime.BillingAdmissionInput) (billing.Authorization, error) {
	return f(ctx, in)
}

func TestExecutorBillingAdmissionDeniesBeforeAnyBackendOpen(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var calls, opens atomic.Int32
	var sawPlan atomic.Bool
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingAdmission = billingAdmissionFunc(func(_ context.Context, in runtime.BillingAdmissionInput) (billing.Authorization, error) {
		calls.Add(1)
		if in.Route == nil || in.Route.Alternatives == nil || in.ALegID == "" || in.TraceID == "" {
			t.Errorf("billing input missing planned request identity: %+v", in)
		}
		sawPlan.Store(true)
		return billing.Authorization{}, errors.New("insufficient credit")
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
	call := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "backend:model"}, Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}}}
	_, err = ex.Execute(context.Background(), call)
	if !errors.Is(err, runtime.ErrBillingAdmissionDenied) {
		t.Fatalf("error = %v, want ErrBillingAdmissionDenied", err)
	}
	if calls.Load() != 1 || opens.Load() != 0 || !sawPlan.Load() {
		t.Fatalf("billing calls=%d backend opens=%d saw plan=%v", calls.Load(), opens.Load(), sawPlan.Load())
	}
}

func TestExecutorBillingAdmissionAllowsNormalStream(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var calls, opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingAdmission = billingAdmissionFunc(func(_ context.Context, in runtime.BillingAdmissionInput) (billing.Authorization, error) {
		calls.Add(1)
		if in.Call.Route.Selector != "backend:model" {
			t.Errorf("route selector = %q", in.Call.Route.Selector)
		}
		return billing.Authorization{}, nil
	})
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventMessageStarted}, {Kind: lipapi.EventResponseFinished}}), nil
			},
		},
	}
	call := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "backend:model"}, Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}}}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || opens.Load() != 1 {
		t.Fatalf("billing calls=%d backend opens=%d", calls.Load(), opens.Load())
	}
}
