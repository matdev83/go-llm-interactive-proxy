package snapshotgen_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/runtimegen"
)

func remediationAdmit(requestID string) authority.RequestAdmission {
	return authority.RequestAdmission{
		RequestID:   requestID,
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

func TestPhase5Remediation_FiveToTwoAdmitUsesLiveCoordinators(t *testing.T) {
	t.Parallel()
	p := snapshotgen.NewPublisher()
	g1, err := snapshotgen.CompileExecutable(runtimegen.GenerationContribution{
		SourceID:                "static",
		Version:                 "g1-five",
		State:                   economics.SnapshotReady,
		ConcurrencyRegistration: concReg("conc", stubConcurrencyProvider{id: "conc"}),
		MaxActiveRequests:       5,
		OperatorRaters: []economics.RaterRegistration{{
			ID: "op-r1", Perspective: metering.PerspectiveOperator, Rater: stubRater{id: "op-r1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if g1.RequestCoord == nil || g1.RequestCoord.Concurrency == nil {
		t.Fatal("g1 must own a live request coordinator concurrency object")
	}
	pub1, err := p.PublishExecutable(g1)
	if err != nil {
		t.Fatal(err)
	}
	pub1.Retain()
	held := p.LookupExecutable(pub1.ID)
	var leases []authoritycoord.CompositeDecision
	for i := range 5 {
		d, err := held.RequestCoord.Admit(context.Background(), remediationAdmit(fmt.Sprintf("old-%d", i)))
		if err != nil {
			t.Fatalf("g1 admit %d: %v", i, err)
		}
		if d.Kind != authority.DecisionAllow {
			t.Fatalf("g1 admit %d kind=%v", i, d.Kind)
		}
		leases = append(leases, d)
	}
	if _, err := held.RequestCoord.Admit(context.Background(), remediationAdmit("old-overflow")); err == nil {
		t.Fatal("g1 max=5 must deny the sixth admit")
	}

	g2, err := snapshotgen.CompileExecutable(runtimegen.GenerationContribution{
		SourceID:                "static",
		Version:                 "g2-two",
		State:                   economics.SnapshotReady,
		ConcurrencyRegistration: concReg("conc", stubConcurrencyProvider{id: "conc"}),
		MaxActiveRequests:       2,
		OperatorRaters: []economics.RaterRegistration{{
			ID: "op-r2", Perspective: metering.PerspectiveOperator, Rater: stubRater{id: "op-r2"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.PublishExecutable(g2); err != nil {
		t.Fatal(err)
	}
	cur := p.CurrentExecutable()
	if cur.RequestCoord == nil || cur.EnforcementMaxActive() != 2 {
		t.Fatalf("current must enforce max=2 via live coordinator, got %+v", cur)
	}
	for i := range 2 {
		if _, err := cur.RequestCoord.Admit(context.Background(), remediationAdmit(fmt.Sprintf("new-%d", i))); err != nil {
			t.Fatalf("g2 admit %d: %v", i, err)
		}
	}
	if _, err := cur.RequestCoord.Admit(context.Background(), remediationAdmit("new-overflow")); err == nil {
		t.Fatal("g2 max=2 must deny the third new admit")
	}
	if held.EnforcementMaxActive() != 5 {
		t.Fatalf("in-flight generation mutated: max=%d", held.EnforcementMaxActive())
	}
	if held.EvidenceObjectID() == cur.EvidenceObjectID() {
		t.Fatal("rating evidence must change with generation")
	}
	for i, d := range leases {
		_ = held.RequestCoord.Release(context.Background(), d.Stack, fmt.Sprintf("old-%d", i))
	}
}
