package authoritycoord_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
)

// Requirement 15.9: panics / malformed enterprise provider responses must be
// isolated and mapped through stable failure posture (fail-closed by default).

func TestRequestCoordinator_IsolatesProviderPanicFailClosed(t *testing.T) {
	t.Parallel()
	panicProv := &fakeRequestProvider{id: "enterprise"}
	panicProv.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		panic("enterprise provider boom")
	}
	prior := &fakeRequestProvider{id: "prior"}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "prior", Class: authoritycoord.PriorityCreditWallet, Provider: prior, Strength: authority.StrengthRequired},
			{ID: "enterprise", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: panicProv, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("expected unavailable error from provider panic")
	}
	var unavail *authoritycoord.ErrUnavailable
	if !errors.As(err, &unavail) {
		t.Fatalf("want ErrUnavailable, got %T %v", err, err)
	}
	if unavail.ProviderID != "enterprise" {
		t.Fatalf("provider id=%q", unavail.ProviderID)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "panic") {
		t.Fatalf("error should mention panic: %v", err)
	}
	if d.Kind != authority.DecisionDeny {
		t.Fatalf("kind=%s want deny", d.Kind)
	}
	if prior.released.Load() != 1 {
		t.Fatalf("prior hold must reverse-compensate on panic; released=%d", prior.released.Load())
	}
}

func TestRequestCoordinator_IsolatesMalformedDecisionKind(t *testing.T) {
	t.Parallel()
	bad := &fakeRequestProvider{id: "malformed"}
	bad.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{Kind: authority.DecisionKind("not-a-kind"), ProviderID: "malformed"}, nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "malformed", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: bad, Strength: authority.StrengthRequired},
		},
	}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("expected unavailable error from malformed decision")
	}
	var unavail *authoritycoord.ErrUnavailable
	if !errors.As(err, &unavail) {
		t.Fatalf("want ErrUnavailable, got %T %v", err, err)
	}
	if unavail.ProviderID != "malformed" {
		t.Fatalf("provider id=%q", unavail.ProviderID)
	}
}

func TestRequestCoordinator_AdvisoryPanicDegradesNotDeny(t *testing.T) {
	t.Parallel()
	adv := &fakeRequestProvider{id: "adv"}
	adv.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		panic("advisory observer panic")
	}
	hard := &fakeRequestProvider{id: "hard"}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "hard", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: hard, Strength: authority.StrengthRequired},
			{ID: "adv", Class: authoritycoord.PriorityAdvisory, Provider: adv, Strength: authority.StrengthAdvisory, FailureBehavior: authority.FailureFailOpen},
		},
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatalf("advisory panic must fail-open: %v", err)
	}
	if d.Kind != authority.DecisionAllow {
		t.Fatalf("kind=%s", d.Kind)
	}
	if d.Readiness != authority.ReadinessDegraded {
		t.Fatalf("readiness=%s want degraded", d.Readiness)
	}
}

func TestRequestCoordinator_IsolatesConcurrencyPanic(t *testing.T) {
	t.Parallel()
	conc := &fakeConcurrencyProvider{
		admit: func(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
			panic("concurrency provider boom")
		},
	}
	coord := &authoritycoord.RequestCoordinator{Concurrency: conc}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("expected unavailable error from concurrency panic")
	}
	var unavail *authoritycoord.ErrUnavailable
	if !errors.As(err, &unavail) {
		t.Fatalf("want ErrUnavailable, got %T %v", err, err)
	}
	if unavail.ProviderID != "concurrency" {
		t.Fatalf("provider id=%q", unavail.ProviderID)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "panic") {
		t.Fatalf("error should mention panic: %v", err)
	}
}

func TestRequestCoordinator_IsolatesMalformedLeaseDecisionKind(t *testing.T) {
	t.Parallel()
	conc := &fakeConcurrencyProvider{
		admit: func(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
			return authority.LeaseDecision{Kind: authority.LeaseDecisionKind("not-a-lease-kind")}, nil
		},
	}
	coord := &authoritycoord.RequestCoordinator{Concurrency: conc}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("expected unavailable error from malformed lease decision")
	}
	var unavail *authoritycoord.ErrUnavailable
	if !errors.As(err, &unavail) {
		t.Fatalf("want ErrUnavailable, got %T %v", err, err)
	}
	if unavail.ProviderID != "concurrency" {
		t.Fatalf("provider id=%q", unavail.ProviderID)
	}
}

func TestAttemptCoordinator_IsolatesProviderPanic(t *testing.T) {
	t.Parallel()
	panicProv := &fakeAttemptProvider{id: "enterprise-attempt"}
	panicProv.admit = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		panic("attempt provider boom")
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "enterprise-attempt", Class: authoritycoord.AttemptPriorityHardSpend, Provider: panicProv, Strength: authority.StrengthRequired},
		},
	}
	_, err := coord.Admit(context.Background(), validAttemptAdmission("b-panic"))
	if err == nil {
		t.Fatal("expected unavailable error from attempt provider panic")
	}
	var unavail *authoritycoord.ErrUnavailable
	if !errors.As(err, &unavail) {
		t.Fatalf("want ErrUnavailable, got %T %v", err, err)
	}
}

func TestCompensationStack_IsolatesReleasePanic(t *testing.T) {
	t.Parallel()
	var stack authoritycoord.CompensationStack
	stack.Push(authoritycoord.StackEntry{
		ProviderID: "ok",
		Handle:     "h-ok",
		Compensate: func(context.Context) error { return nil },
	})
	stack.Push(authoritycoord.StackEntry{
		ProviderID: "boom",
		Handle:     "h-boom",
		Compensate: func(context.Context) error {
			panic("release panic")
		},
	})
	failed := stack.ReverseCompensate(context.Background(), time.Second)
	if len(failed) != 1 {
		t.Fatalf("failed=%+v want one isolated panic failure", failed)
	}
	if failed[0].ProviderID != "boom" {
		t.Fatalf("provider=%q", failed[0].ProviderID)
	}
	if !strings.Contains(strings.ToLower(failed[0].Err.Error()), "panic") {
		t.Fatalf("err=%v", failed[0].Err)
	}
}
