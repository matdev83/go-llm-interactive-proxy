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
// for the standard dogfood composition.
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
	caps := rt.Capabilities()
	if caps.SnapshotGenerationID == 0 {
		t.Fatal("SnapshotGenerationID must be non-zero on dogfood facade")
	}
	switch caps.ExecutableState {
	case cp.CapabilityReady, cp.CapabilityDisabled, cp.CapabilityUnavailable, cp.CapabilityDegraded:
	default:
		t.Fatalf("ExecutableState=%q outside closed capability vocabulary", caps.ExecutableState)
	}
	report := rt.ReadinessReport()
	if report == nil {
		t.Fatal("ReadinessReport must be exposed on facade")
	}
	if _, err := report.Report(ctx); err != nil {
		t.Fatalf("ReadinessReport.Report: %v", err)
	}
	if caps.ProductionMetering {
		t.Fatal("ProductionMetering must be false without MeteringRecorder")
	}
	if caps.ProductionRater {
		t.Fatal("ProductionRater must be false without RaterRegistrations")
	}
	if caps.TrafficObservers || caps.UsageObservers || caps.ProductionEvidenceSink {
		t.Fatal("observer attachment flags must be false without production injections")
	}
	if caps.ProductionMeteringQuerier {
		t.Fatal("ProductionMeteringQuerier must be false without MeteringQuerier")
	}
}
