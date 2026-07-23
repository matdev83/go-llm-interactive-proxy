package lipruntime_test

import (
	"context"
	"io"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

// TestCapabilityReporting_DogfoodFacadeHostSnapshot freezes externally observable
// runtime capability reporting on the public facade path (derived host snapshot)
// for the standard dogfood composition. Executable-generation economics detail
// with injected raters remains covered by TestPhase55_FacadeExposesExecutableGenerationWithoutInternalTypes.
func TestCapabilityReporting_DogfoodFacadeHostSnapshot(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "config", "examples", "dogfood-local-stub.yaml")
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{ConfigPath: path, LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })

	if !rt.Ready() {
		t.Fatal("Ready must be true with active generation")
	}
	if rt.ExecutorView() == nil {
		t.Fatal("ExecutorView required for capability-bearing runtime")
	}
	if id := rt.SnapshotGenerationID(); id == 0 {
		t.Fatal("SnapshotGenerationID must be non-zero on dogfood facade")
	}

	// Without an injected operator rater, executable economics may be absent/disabled;
	state := rt.ExecutableGenerationState()
	switch state {
	case cp.CapabilityReady, cp.CapabilityDisabled, cp.CapabilityUnavailable, cp.CapabilityDegraded:
		// closed vocabulary only
	default:
		t.Fatalf("ExecutableGenerationState=%q outside closed capability vocabulary", state)
	}

	report := rt.ReadinessReport()
	if report == nil {
		t.Fatal("ReadinessReport must be exposed on facade")
	}
	if _, err := report.Report(ctx); err != nil {
		t.Fatalf("ReadinessReport.Report: %v", err)
	}

	// Plain dogfood Build has no production observer/metering injection.
	if rt.HasProductionMetering() {
		t.Fatal("HasProductionMetering must be false without MeteringRecorder")
	}
	if rt.HasProductionRater() {
		t.Fatal("HasProductionRater must be false without RaterRegistrations")
	}
	if rt.HasTrafficObservers() || rt.HasUsageObservers() || rt.HasProductionEvidenceSink() {
		t.Fatal("observer attachment flags must be false without production injections")
	}
	if rt.HasProductionMeteringQuerier() {
		t.Fatal("HasProductionMeteringQuerier must be false without MeteringQuerier")
	}
}
