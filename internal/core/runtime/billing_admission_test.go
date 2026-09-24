package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
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
	if errors.Is(err, runtime.ErrBillingAdmissionDenied) || opens.Load() != 1 {
		t.Fatalf("stock runtime must not infer billing from absent ports: error=%v provider_opens=%d", err, opens.Load())
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
	if errors.Is(err, runtime.ErrBillingAdmissionDenied) || opens.Load() != 1 {
		t.Fatalf("stock runtime must not infer billing from absent ports: error=%v provider_opens=%d", err, opens.Load())
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

// TestExecutorDegradedCreditFailsClosedBeforeProviderOpen pins the Finding 4
// runtime seam: a degraded/unavailable cheap-screen outcome (the only posture
// a binary gate can express for public CreditDegraded) rejects with the
// unavailable classification before route planning, never as ordinary allow
// and never reaching provider open or exposure admission.
func TestExecutorDegradedCreditFailsClosedBeforeProviderOpen(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var calls, opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingIdentity.AccountID = func(context.Context, lipapi.Call) string { return "acct-screen" }
	// Adapter-shaped degraded outcome: unavailable class, never denied.
	ex.BillingCreditGate = creditGateFunc(func(_ context.Context, accountID string) error {
		calls.Add(1)
		if accountID != "acct-screen" {
			t.Fatalf("account id = %q", accountID)
		}
		return fmt.Errorf("binding degraded account %q: %w", accountID, billing.ErrCreditScreenUnavailable)
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
	if !errors.Is(err, runtime.ErrBillingAdmissionDenied) {
		t.Fatalf("error = %v, want ErrBillingAdmissionDenied", err)
	}
	if !errors.Is(err, runtime.ErrBillingCreditScreenUnavailable) {
		t.Fatalf("error = %v, want ErrBillingCreditScreenUnavailable", err)
	}
	if errors.Is(err, runtime.ErrBillingCreditScreenDenied) {
		t.Fatalf("degraded credit must not classify as denied: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("credit-screen calls = %d, want 1", calls.Load())
	}
	if opens.Load() != 0 {
		t.Fatalf("provider opens = %d, want 0 (rejected before provider open)", opens.Load())
	}
}

// TestCanonicalWireAdmissionScopeParity proves the canonical admission input
// carries the same frozen trusted scope and credited account as the wire
// path: scope from the trusted request context, account from the shared
// wire fallback chain.
func TestCanonicalWireAdmissionScopeParity(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	trusted := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("user-parity"),
		TenantID:    scope.Known("tenant-parity"),
		Origin:      scope.OriginClient,
	}
	var canonical, wire runtime.BillingExposureAdmissionInput
	var canonicalCalls, wireCalls atomic.Int32
	identity := runtime.BillingIdentity{
		AccountID:     func(context.Context, lipapi.Call) string { return "acct-1" },
		WireAccountID: func(context.Context, scope.PrincipalScopeView) string { return "acct-1" },
		WireBounded:   true,
	}
	newExecutor := func(capture *runtime.BillingExposureAdmissionInput, calls *atomic.Int32) *runtime.Executor {
		ex := runtime.TestExecutor()
		ex.Store = st
		ex.Bus = hooks.New(hooks.Config{})
		ex.Rand = routing.NewSeededRng(1)
		ex.BillingIdentity = identity
		ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
		ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in runtime.BillingExposureAdmissionInput) (billing.CallExposure, error) {
			calls.Add(1)
			*capture = in
			return billing.CallExposure{}, nil
		})
		ex.Backends = map[string]execbackend.Backend{
			"backend": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return nil, errors.New("open-tag")
				},
			},
		}
		return ex
	}

	maxOut := 64
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "backend:model"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
		Options:  lipapi.GenerationOptions{MaxOutputTokens: &maxOut},
	}
	sctx := scope.WithScope(context.Background(), trusted)
	_, err = newExecutor(&canonical, &canonicalCalls).Execute(sctx, call)
	if err == nil || !strings.Contains(err.Error(), "open-tag") {
		t.Fatalf("canonical execute err=%v, want flow through admission to open", err)
	}
	if canonicalCalls.Load() != 1 {
		t.Fatalf("canonical admissions = %d, want 1", canonicalCalls.Load())
	}

	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	wireArgs := runtime.WireBillingExposureArgs{
		BillingCallID:   callID,
		Scope:           trusted,
		MaxOutputTokens: &maxOut,
	}
	wireEx, wireErr := newExecutor(&wire, &wireCalls).AuthorizeWireBilling(sctx, wireArgs)
	if wireErr != nil {
		t.Fatalf("wire authorize: %v", wireErr)
	}
	if wireCalls.Load() != 1 {
		t.Fatalf("wire admissions = %d, want 1", wireCalls.Load())
	}
	if wireEx.AccountID != "acct-1" {
		t.Fatalf("wire exposure account = %q, want credited acct-1", wireEx.AccountID)
	}

	if !canonical.Scope.PrincipalID.Equal(trusted.PrincipalID) {
		t.Fatalf("canonical scope = %+v, want trusted request scope", canonical.Scope.PrincipalID)
	}
	if canonical.AccountID != "acct-1" {
		t.Fatalf("canonical account = %q, want credited acct-1", canonical.AccountID)
	}
	if !wire.Scope.PrincipalID.Equal(trusted.PrincipalID) || wire.AccountID != "acct-1" {
		t.Fatalf("wire scope/account = %+v/%q, want trusted/credited", wire.Scope.PrincipalID, wire.AccountID)
	}
	if canonical.Scope.PrincipalID != wire.Scope.PrincipalID || canonical.AccountID != wire.AccountID {
		t.Fatalf("canonical (%+v/%q) and wire (%+v/%q) admission identity must match",
			canonical.Scope.PrincipalID, canonical.AccountID, wire.Scope.PrincipalID, wire.AccountID)
	}
}
