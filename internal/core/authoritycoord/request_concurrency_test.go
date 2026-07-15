package authoritycoord_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
)

type fakeConcurrencyProvider struct {
	admit    func(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error)
	released atomic.Int32
	renewed  atomic.Int32
}

func (f *fakeConcurrencyProvider) AdmitLease(ctx context.Context, in authority.LeaseAdmission) (authority.LeaseDecision, error) {
	if f.admit != nil {
		return f.admit(ctx, in)
	}
	return authority.LeaseDecision{
		Kind:       authority.LeaseAllow,
		LeaseID:    "lease-1",
		Generation: 1,
		ExpiresAt:  time.Now().Add(time.Minute),
	}, nil
}

func (f *fakeConcurrencyProvider) RenewLease(context.Context, authority.LeaseRenew) (authority.LeaseDecision, error) {
	f.renewed.Add(1)
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "lease-1", Generation: 2, ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (f *fakeConcurrencyProvider) ReleaseLease(context.Context, authority.LeaseRelease) error {
	f.released.Add(1)
	return nil
}

func (f *fakeConcurrencyProvider) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

func TestRequestCoordinator_AdmitCapturesLeaseAndPassesLifecycle(t *testing.T) {
	t.Parallel()
	var gotLifecycle authority.LifecycleScope
	var gotParent string
	conc := &fakeConcurrencyProvider{
		admit: func(_ context.Context, in authority.LeaseAdmission) (authority.LeaseDecision, error) {
			gotLifecycle = in.Lifecycle
			gotParent = in.ParentLeaseID
			return authority.LeaseDecision{
				Kind:        authority.LeaseAllow,
				LeaseID:     "lease-aux",
				Generation:  3,
				ExpiresAt:   time.Now().Add(time.Minute),
				RenewBefore: 10 * time.Second,
				TTL:         time.Minute,
			}, nil
		},
	}
	coord := &authoritycoord.RequestCoordinator{Concurrency: conc}
	in := validRequestAdmission()
	in.Lifecycle = authority.LifecycleAuxiliaryRequest
	in.ParentLeaseID = "parent-lease"
	d, err := coord.Admit(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if gotLifecycle != authority.LifecycleAuxiliaryRequest || gotParent != "parent-lease" {
		t.Fatalf("lifecycle=%q parent=%q", gotLifecycle, gotParent)
	}
	if d.Lease.LeaseID != "lease-aux" || d.Lease.Generation != 3 {
		t.Fatalf("lease=%+v", d.Lease)
	}
	if len(d.Stack.Handles()) != 1 || d.Stack.Handles()[0] != "lease-aux" {
		t.Fatalf("handles=%v", d.Stack.Handles())
	}
}

func TestRequestCoordinator_SettleDoesNotReleaseLease(t *testing.T) {
	t.Parallel()
	conc := &fakeConcurrencyProvider{}
	quota := &fakeRequestProvider{id: "quota"}
	coord := &authoritycoord.RequestCoordinator{
		Concurrency: conc,
		Slots: []authoritycoord.RequestSlot{{
			ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: quota,
		}},
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err != nil {
		t.Fatal(err)
	}
	if err := coord.Settle(context.Background(), authority.RequestSettlement{RequestID: "req-1", Handles: d.Stack.Handles()}); err != nil {
		t.Fatal(err)
	}
	if conc.released.Load() != 0 {
		t.Fatalf("Settle must not release concurrency lease; released=%d", conc.released.Load())
	}
	if err := coord.ReleaseLease(context.Background(), d.Lease.LeaseID, "req-1", "settled"); err != nil {
		t.Fatal(err)
	}
	if conc.released.Load() != 1 {
		t.Fatalf("ReleaseLease released=%d want 1", conc.released.Load())
	}
}
