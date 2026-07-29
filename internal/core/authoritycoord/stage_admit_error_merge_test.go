package authoritycoord_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// Stage-admit errors after a successful concurrency prefix must return the enriched
// composite (lease, aggregated readiness, bound versions, stage deny metadata) — not
// the bare stage evaluator output.
func TestRequestCoordinator_StageAdmitErrorPreservesConcurrencyPrefix(t *testing.T) {
	t.Parallel()
	leaseBV := economics.PolicySnapshotRef{VersionRef: economics.VersionRef{Version: "lease-v1"}}
	conc := &fakeConcurrencyProvider{
		admit: func(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
			return authority.LeaseDecision{
				Kind:         authority.LeaseAllow,
				LeaseID:      "lease-prefix",
				Generation:   1,
				ExpiresAt:    time.Now().Add(time.Minute),
				Readiness:    authority.ReadinessReady,
				BoundVersion: leaseBV,
			}, nil
		},
	}
	denyQuota := &fakeRequestProvider{
		id: "quota",
		admit: func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
			return authority.Decision{Kind: authority.DecisionDeny, ProviderID: "quota"}, nil
		},
	}
	coord := &authoritycoord.RequestCoordinator{
		Concurrency: conc,
		Slots: []authoritycoord.RequestSlot{{
			ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: denyQuota,
		}},
		CleanupTimeout: time.Second,
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if !authoritycoord.IsDenied(err) {
		t.Fatalf("want ErrDenied, got err=%v d=%+v", err, d)
	}
	if d.Lease.LeaseID != "lease-prefix" {
		t.Fatalf("Lease=%+v want concurrency prefix preserved on stage-admit error", d.Lease)
	}
	if d.DeniedBy != "quota" {
		t.Fatalf("DeniedBy=%q want stage provider id", d.DeniedBy)
	}
	if d.Kind != authority.DecisionDeny {
		t.Fatalf("Kind=%s want deny", d.Kind)
	}
	if len(d.BoundVersions) != 1 || d.BoundVersions[0].Version != "lease-v1" {
		t.Fatalf("BoundVersions=%+v want lease prefix bound version", d.BoundVersions)
	}
	if d.Readiness != authority.ReadinessReady {
		t.Fatalf("Readiness=%s want aggregated readiness from concurrency prefix", d.Readiness)
	}
	if conc.released.Load() != 1 {
		t.Fatalf("concurrency released=%d want 1 (stage error must reverse-compensate prefix holds)", conc.released.Load())
	}
}

func TestRequestCoordinator_StageAdmitUnavailablePreservesConcurrencyPrefix(t *testing.T) {
	t.Parallel()
	conc := &fakeConcurrencyProvider{
		admit: func(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
			return authority.LeaseDecision{
				Kind:       authority.LeaseAllow,
				LeaseID:    "lease-prefix",
				Generation: 1,
				ExpiresAt:  time.Now().Add(time.Minute),
			}, nil
		},
	}
	failQuota := &fakeRequestProvider{
		id: "quota",
		admit: func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
			return authority.Decision{}, errors.New("quota unavailable")
		},
	}
	coord := &authoritycoord.RequestCoordinator{
		Concurrency: conc,
		Slots: []authoritycoord.RequestSlot{{
			ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: failQuota,
		}},
		CleanupTimeout: time.Second,
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	var unavail *authoritycoord.ErrUnavailable
	if !errors.As(err, &unavail) || unavail.ProviderID != "quota" {
		t.Fatalf("want ErrUnavailable from quota, got %v", err)
	}
	if d.Lease.LeaseID != "lease-prefix" {
		t.Fatalf("Lease=%+v want concurrency prefix preserved on stage-admit error", d.Lease)
	}
	if d.DeniedBy != "quota" {
		t.Fatalf("DeniedBy=%q want stage provider id", d.DeniedBy)
	}
	if d.Kind != authority.DecisionDeny {
		t.Fatalf("Kind=%s want deny", d.Kind)
	}
}
