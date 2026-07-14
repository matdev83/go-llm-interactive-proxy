package authoritycoord_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type fakeRequestProvider struct {
	id       string
	admit    func(context.Context, authority.RequestAdmission) (authority.Decision, error)
	released atomic.Int32
}

func (f *fakeRequestProvider) AdmitRequest(ctx context.Context, in authority.RequestAdmission) (authority.Decision, error) {
	if f.admit != nil {
		return f.admit(ctx, in)
	}
	return authority.Decision{
		Kind: authority.DecisionAllow,
		Reservations: []authority.Reservation{{
			Handle: f.id + "-h",
			Kind:   authority.ReservationQuota,
		}},
	}, nil
}

func (f *fakeRequestProvider) SettleRequest(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
	return authority.Settlement{Kind: authority.SettlementFinal}, nil
}

func (f *fakeRequestProvider) ReleaseRequest(ctx context.Context, in authority.RequestRelease) error {
	_ = ctx
	_ = in
	f.released.Add(1)
	return nil
}

func TestRequestCoordinator_PriorityAndReverseCompensate(t *testing.T) {
	t.Parallel()
	wallet := &fakeRequestProvider{id: "wallet"}
	quota := &fakeRequestProvider{id: "quota"}
	quota.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{Kind: authority.DecisionDeny, ProviderID: "quota"}, nil
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{
			{ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: quota, Strength: authority.StrengthRequired},
			{ID: "wallet", Class: authoritycoord.PriorityCreditWallet, Provider: wallet, Strength: authority.StrengthRequired},
		},
		CleanupTimeout: time.Second,
	}
	_, err := coord.Admit(context.Background(), validRequestAdmission())
	if !authoritycoord.IsDenied(err) {
		t.Fatalf("got %v", err)
	}
	if wallet.released.Load() != 1 {
		t.Fatalf("wallet released=%d want 1 (reverse compensate)", wallet.released.Load())
	}
}

func TestRequestCoordinator_NilConcurrencySkipped(t *testing.T) {
	t.Parallel()
	quota := &fakeRequestProvider{id: "quota"}
	coord := &authoritycoord.RequestCoordinator{
		Concurrency: nil,
		Slots: []authoritycoord.RequestSlot{
			{ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: quota},
		},
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != authority.DecisionAllow || len(d.Stack.Handles()) != 1 {
		t.Fatalf("decision=%+v", d)
	}
}

func TestRequestCoordinator_AdvisoryFailOpen(t *testing.T) {
	t.Parallel()
	adv := &fakeRequestProvider{id: "adv"}
	adv.admit = func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
		return authority.Decision{}, errors.New("observer down")
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
		t.Fatal(err)
	}
	if d.Kind != authority.DecisionAllow {
		t.Fatalf("kind=%s", d.Kind)
	}
}

func TestMergeClampsNonWidening(t *testing.T) {
	t.Parallel()
	coord := &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "a", Class: authoritycoord.PriorityQuotaBudgetRate,
			Provider: &fakeRequestProvider{id: "a", admit: func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
				return authority.Decision{
					Kind:   authority.DecisionAllow,
					Clamps: []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: 100}},
				}, nil
			}},
		}, {
			ID: "b", Class: authoritycoord.PriorityAdvisory,
			Provider: &fakeRequestProvider{id: "b", admit: func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
				return authority.Decision{
					Kind:   authority.DecisionAllow,
					Clamps: []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: 50}},
				}, nil
			}},
		}},
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Clamps) != 1 || d.Clamps[0].Value != 50 {
		t.Fatalf("clamps=%+v", d.Clamps)
	}
}

func validRequestAdmission() authority.RequestAdmission {
	return authority.RequestAdmission{
		RequestID:   "req-1",
		ALegID:      "a-1",
		Perspective: metering.PerspectiveCustomer,
		Lifecycle:   metering.LifecycleLogicalRequest,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveCustomer,
			Boundary:    metering.BoundaryFrontendIngress,
			Lifecycle:   metering.LifecycleLogicalRequest,
		},
	}
}
