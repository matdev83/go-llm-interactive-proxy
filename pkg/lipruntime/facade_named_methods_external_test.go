package lipruntime_test

import (
	"context"
	"io"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

// TestExternalFacade_NamedCapabilityMethods matches historical public Runtime
// accessors: each named method delegates through Capabilities()/Host and must
// remain source-compatible for external modules.
func TestExternalFacade_NamedCapabilityMethods(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	evidence := &namedMethodEvidence{}
	rt, err := lipruntime.Build(ctx, lipruntime.Options{
		ConfigPath:       repoConfigPath(t),
		LogWriter:        io.Discard,
		TrafficObservers: []traffic.Observer{noopTraffic{}},
		UsageObservers:   []usage.Observer{noopUsage{}},
		MeteringRecorder: namedMethodMeter{},
		MeteringQuerier:  facadeQuerier{},
		EvidenceSink:     evidence,
		RaterRegistrations: []economics.RaterRegistration{{
			ID: "named-method-rater", Perspective: metering.PerspectiveOperator, Rater: namedMethodRater{},
		}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })

	caps := rt.Capabilities()
	if !rt.HasTrafficObservers() || !caps.TrafficObservers {
		t.Fatal("HasTrafficObservers must match Capabilities")
	}
	if !rt.HasUsageObservers() || !caps.UsageObservers {
		t.Fatal("HasUsageObservers must match Capabilities")
	}
	if !rt.HasProductionMetering() || !caps.ProductionMetering {
		t.Fatal("HasProductionMetering must match Capabilities")
	}
	if !rt.HasProductionEvidenceSink() || !caps.ProductionEvidenceSink {
		t.Fatal("HasProductionEvidenceSink must match Capabilities")
	}
	if !rt.HasProductionRater() || !caps.ProductionRater {
		t.Fatal("HasProductionRater must match Capabilities")
	}
	if !rt.HasProductionMeteringQuerier() || !caps.ProductionMeteringQuerier || rt.MeteringQuerier() == nil {
		t.Fatal("HasProductionMeteringQuerier / MeteringQuerier must be wired")
	}
	if rt.SnapshotGenerationID() == 0 || rt.SnapshotGenerationID() != caps.SnapshotGenerationID {
		t.Fatalf("SnapshotGenerationID=%d caps=%d", rt.SnapshotGenerationID(), caps.SnapshotGenerationID)
	}
	if rt.SnapshotUsageVersion() != caps.SnapshotUsageVersion {
		t.Fatalf("SnapshotUsageVersion=%q caps=%q", rt.SnapshotUsageVersion(), caps.SnapshotUsageVersion)
	}
	if rt.ExecutableGenerationID() == 0 || rt.ExecutableGenerationID() != caps.ExecutableGenerationID {
		t.Fatalf("ExecutableGenerationID=%d caps=%d", rt.ExecutableGenerationID(), caps.ExecutableGenerationID)
	}
	if rt.ExecutableGenerationVersion() == "" || rt.ExecutableGenerationVersion() != caps.ExecutableVersion {
		t.Fatalf("ExecutableGenerationVersion=%q caps=%q", rt.ExecutableGenerationVersion(), caps.ExecutableVersion)
	}
	if rt.ExecutableEvidenceObjectID() == "" || rt.ExecutableEvidenceObjectID() != caps.ExecutableEvidenceObjectID {
		t.Fatalf("ExecutableEvidenceObjectID=%q caps=%q", rt.ExecutableEvidenceObjectID(), caps.ExecutableEvidenceObjectID)
	}
	state := rt.ExecutableGenerationState()
	switch state {
	case controlplane.CapabilityReady, controlplane.CapabilityDisabled,
		controlplane.CapabilityUnavailable, controlplane.CapabilityDegraded:
	default:
		t.Fatalf("ExecutableGenerationState=%q outside closed vocabulary", state)
	}
	if state != caps.ExecutableState {
		t.Fatalf("ExecutableGenerationState=%q caps=%q", state, caps.ExecutableState)
	}
	if rt.ReadinessReport() == nil {
		t.Fatal("ReadinessReport must be exposed")
	}
}

func TestExternalFacade_NilRuntimeNamedMethods(t *testing.T) {
	t.Parallel()
	var rt *lipruntime.Runtime
	if rt.HasProductionMetering() || rt.HasTrafficObservers() || rt.HasUsageObservers() ||
		rt.HasProductionEvidenceSink() || rt.HasProductionRater() || rt.HasProductionMeteringQuerier() {
		t.Fatal("nil Runtime Has* methods must be false")
	}
	if rt.MeteringQuerier() != nil || rt.ReadinessReport() != nil {
		t.Fatal("nil Runtime querier/report must be nil")
	}
	if rt.SnapshotGenerationID() != 0 || rt.ExecutableGenerationID() != 0 {
		t.Fatal("nil Runtime generation ids must be 0")
	}
	if rt.SnapshotUsageVersion() != "" || rt.ExecutableGenerationVersion() != "" || rt.ExecutableEvidenceObjectID() != "" {
		t.Fatal("nil Runtime string accessors must be empty")
	}
	if rt.ExecutableGenerationState() != controlplane.CapabilityDisabled {
		t.Fatalf("nil Runtime ExecutableGenerationState=%q want disabled", rt.ExecutableGenerationState())
	}
	caps := rt.Capabilities()
	if caps.ExecutableState != controlplane.CapabilityDisabled {
		t.Fatalf("nil Runtime Capabilities.ExecutableState=%q", caps.ExecutableState)
	}
}

type namedMethodMeter struct{}

func (namedMethodMeter) Append(context.Context, metering.Fact) error { return nil }

type namedMethodEvidence struct{}

func (*namedMethodEvidence) RecordPolicyDecision(context.Context, policydecision.Record) error {
	return nil
}
func (*namedMethodEvidence) RecordAccountingAuthority(context.Context, controlplane.Event) error {
	return nil
}

type namedMethodRater struct{}

func (namedMethodRater) Rate(_ context.Context, req economics.RatingRequest) (economics.RatingResult, error) {
	res := economics.RatingResult{
		Money:       economics.Money{NanoUnits: 1, Currency: "USD", Present: true},
		Source:      "named-method",
		Perspective: req.Perspective,
		RaterID:     "named-method-rater",
		Version:     economics.VersionRef{ID: "named-method", Version: "v1"},
	}
	if err := res.ValidateFor(req); err != nil {
		return economics.RatingResult{}, err
	}
	return res, nil
}
