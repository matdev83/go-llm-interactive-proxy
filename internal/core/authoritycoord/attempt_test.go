package authoritycoord_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type fakeAttemptProvider struct {
	id       string
	admit    func(context.Context, authority.AttemptAdmission) (authority.Decision, error)
	released atomic.Int32
}

func (f *fakeAttemptProvider) AdmitAttempt(ctx context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	if f.admit != nil {
		return f.admit(ctx, in)
	}
	return authority.Decision{
		Kind:         authority.DecisionAllow,
		Reservations: []authority.Reservation{{Handle: f.id + "-h", Kind: authority.ReservationSpend}},
	}, nil
}

func (f *fakeAttemptProvider) SettleAttempt(context.Context, authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.Settlement{Kind: authority.SettlementFinal}, nil
}

func (f *fakeAttemptProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	f.released.Add(1)
	return nil
}

func TestAttemptCoordinator_IndependentBLegsAndCompensate(t *testing.T) {
	t.Parallel()
	spend := &fakeAttemptProvider{id: "spend"}
	deny := &fakeAttemptProvider{id: "quota"}
	deny.admit = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{Kind: authority.DecisionDeny}, nil
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "spend", Class: authoritycoord.AttemptPriorityHardSpend, Provider: spend},
			{ID: "quota", Class: authoritycoord.AttemptPriorityQuotaRate, Provider: deny},
		},
		CleanupTimeout: time.Second,
	}
	_, err := coord.Admit(context.Background(), validAttemptAdmission("b1"))
	if !authoritycoord.IsDenied(err) {
		t.Fatalf("got %v", err)
	}
	if spend.released.Load() != 1 {
		t.Fatalf("spend released=%d", spend.released.Load())
	}

	// Second B-leg with allow-only still independent.
	okCoord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "spend2", Class: authoritycoord.AttemptPriorityHardSpend, Provider: &fakeAttemptProvider{id: "spend2"}},
		},
	}
	d, err := okCoord.Admit(context.Background(), validAttemptAdmission("b2"))
	if err != nil || d.Kind != authority.DecisionAllow {
		t.Fatalf("b2 err=%v d=%+v", err, d)
	}
}

func TestAttemptCoordinator_AggregatesBoundVersions(t *testing.T) {
	t.Parallel()
	prov := &fakeAttemptProvider{id: "spend"}
	prov.admit = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind: authority.DecisionAllow,
			BoundVersions: []economics.PolicySnapshotRef{{
				VersionRef: economics.VersionRef{ID: "usage_authority", Version: "v1"},
				PolicyID:   "usage_authority",
			}},
			Reservations: []authority.Reservation{{Handle: "h1", Kind: authority.ReservationSpend}},
		}, nil
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "spend", Class: authoritycoord.AttemptPriorityHardSpend, Provider: prov},
		},
	}
	d, err := coord.Admit(context.Background(), validAttemptAdmission("b-bound"))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if len(d.BoundVersions) != 1 || d.BoundVersions[0].Version != "v1" {
		t.Fatalf("bound versions = %+v", d.BoundVersions)
	}
}

func validAttemptAdmission(bleg string) authority.AttemptAdmission {
	return authority.AttemptAdmission{
		RequestID:   "req-1",
		AttemptID:   bleg,
		BLegID:      bleg,
		ALegID:      "a-1",
		Perspective: metering.PerspectiveOperator,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
		},
	}
}
