package runtimebundle

import (
	"testing"

	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

func TestMergeCandidateBuildOptions_NilOverlay_ReturnsIndependentPointerAndIsolatesFeaturePlanes(t *testing.T) {
	t.Parallel()

	process := &BuildOptions{
		ReplaceCandidateSurface: false,
	}
	merged := mergeCandidateBuildOptions(process, nil)
	if merged == nil {
		t.Fatal("expected non-nil merged options")
	}
	if merged == process {
		t.Fatal("mergeCandidateBuildOptions(process, nil) must return an independent pointer, got shared pointer")
	}

	// Mutating merged.FeaturePlanes must not affect process.
	contrib := lipfeature.NewContributionSet()
	frozen := contrib.Freeze()
	merged.FeaturePlanes = frozen

	if !process.FeaturePlanes.IsZero() {
		t.Fatalf("mutating merged.FeaturePlanes mutated process.FeaturePlanes: %+v", process.FeaturePlanes)
	}
}

func TestMergeCandidateBuildOptions_OverlaySemantics_PreservesOrReplacesFeaturePlanes(t *testing.T) {
	t.Parallel()

	processPlanes := lipfeature.NewContributionSet().Freeze()
	process := &BuildOptions{
		FeaturePlanes: processPlanes,
	}

	// Case 1: Overlay with ReplaceCandidateSurface == false and zero FeaturePlanes preserves process planes.
	overlayNoReplace := &BuildOptions{
		ReplaceCandidateSurface: false,
	}
	merged1 := mergeCandidateBuildOptions(process, overlayNoReplace)
	if merged1 == process {
		t.Fatal("expected independent pointer")
	}
	if merged1.FeaturePlanes.IsZero() {
		t.Fatal("expected process FeaturePlanes to be preserved when overlay FeaturePlanes is zero and ReplaceCandidateSurface is false")
	}

	// Case 2: Overlay with non-zero FeaturePlanes overrides process planes.
	overlayPlanes := lipfeature.NewContributionSet().Freeze()
	overlayWithPlanes := &BuildOptions{
		FeaturePlanes:           overlayPlanes,
		ReplaceCandidateSurface: false,
	}
	merged2 := mergeCandidateBuildOptions(process, overlayWithPlanes)
	if merged2.FeaturePlanes.IsZero() {
		t.Fatal("expected overlay FeaturePlanes to be present")
	}

	// Case 3: Overlay with ReplaceCandidateSurface == true replaces even with zero FeaturePlanes.
	overlayReplaceTrue := &BuildOptions{
		ReplaceCandidateSurface: true,
	}
	merged3 := mergeCandidateBuildOptions(process, overlayReplaceTrue)
	if !merged3.FeaturePlanes.IsZero() {
		t.Fatal("expected zero FeaturePlanes when ReplaceCandidateSurface is true and overlay FeaturePlanes is zero")
	}

	// Case 4: process == nil
	if res := mergeCandidateBuildOptions(nil, nil); res != nil {
		t.Fatalf("expected nil when both are nil, got %+v", res)
	}
	overlayOnly := &BuildOptions{ReplaceCandidateSurface: true}
	if res := mergeCandidateBuildOptions(nil, overlayOnly); res != overlayOnly {
		t.Fatalf("expected overlay pointer when process is nil, got %+v", res)
	}
}

func TestMergeCandidateBuildOptions_ParallelConcurrentMerge(t *testing.T) {
	t.Parallel()

	process := &BuildOptions{
		ReplaceCandidateSurface: false,
	}

	const goroutines = 64
	done := make(chan struct{}, goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer func() { done <- struct{}{} }()
			for range 50 {
				merged := mergeCandidateBuildOptions(process, nil)
				if merged == process {
					t.Errorf("got shared pointer in goroutine %d", idx)
					return
				}
				contrib := lipfeature.NewContributionSet()
				merged.FeaturePlanes = contrib.Freeze()
			}
		}(i)
	}

	for range goroutines {
		<-done
	}

	if !process.FeaturePlanes.IsZero() {
		t.Fatalf("process FeaturePlanes mutated during parallel merges")
	}
}
