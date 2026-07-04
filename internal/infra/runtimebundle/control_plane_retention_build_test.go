package runtimebundle_test

import (
	"testing"

	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

// TestBuild_ControlPlaneRetention_StartupKeepsStatusReady verifies that a Build
// with retention enabled runs the one-shot startup maintenance pass against an
// empty memory store, leaves the retention handle wired, and keeps capability
// status ready (design "Retention and Redaction Flow"; requirements 6.1, 7.2).
func TestBuild_ControlPlaneRetention_StartupKeepsStatusReady(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneBuildConfig()
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "memory"
	cfg.ControlPlane.RecordingPolicy = "best_effort"
	cfg.ControlPlane.Retention.Enabled = true
	cfg.ControlPlane.Retention.Window = "1h"
	built := buildControlPlaneBundle(t, cfg)
	// closers disposed via buildControlPlaneBundle t.Cleanup

	if built.ControlPlaneRetention == nil {
		t.Fatalf("retention: expected ControlPlaneRetention handle wired")
	}
	if built.ControlPlaneStatus == nil {
		t.Fatalf("retention: expected ControlPlaneStatus handle wired")
	}
	snap := built.ControlPlaneStatus.Snapshot()
	if snap.State != cp.CapabilityReady {
		t.Fatalf("retention: startup pass on empty memory store must keep status ready, got %q", snap.State)
	}
}
