package runtimebundle

import (
	"context"
	"slices"
	"testing"

	coresg "github.com/matdev83/go-llm-interactive-proxy/internal/infra/secretguardcompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	sdksg "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

type dummyOptLifecycle struct {
	id string
}

func (dummyOptLifecycle) Start(context.Context) error { return nil }
func (dummyOptLifecycle) Stop(context.Context) error  { return nil }

type stubOptEnv struct{ val string }

func (s stubOptEnv) Lookup(name string) (string, bool) { return s.val, true }
func (s stubOptEnv) Snapshot() []string                { return []string{s.val} }

type stubOptObserver struct{ val string }

func (s stubOptObserver) OnSecretDecision(context.Context, sdksg.DecisionEvent) error {
	return nil
}

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
	overlayOnly := &BuildOptions{
		ReplaceCandidateSurface: true,
		FeaturePlanes:           overlayPlanes,
	}
	res := mergeCandidateBuildOptions(nil, overlayOnly)
	if res == nil {
		t.Fatal("expected non-nil merged options when overlay is non-nil")
	}
	if res == overlayOnly {
		t.Fatal("mergeCandidateBuildOptions(nil, overlay) must return an independent pointer, got shared pointer")
	}
	if res.ReplaceCandidateSurface {
		t.Fatal("expected ReplaceCandidateSurface to be reset to false on returned options")
	}
	if res.FeaturePlanes.IsZero() {
		t.Fatal("expected FeaturePlanes from overlay to be preserved")
	}
}

func TestMergeCandidateBuildOptions_ProcessNilOverlayNonNil_IsolationAndCloning(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	planes := lipfeature.NewContributionSet().Freeze()
	env := stubOptEnv{val: "env-val"}
	obs := stubOptObserver{val: "obs-val"}
	lc1 := dummyOptLifecycle{id: "lc1"}
	lc2 := dummyOptLifecycle{id: "lc2"}

	overlay := &BuildOptions{
		PluginRegistry:          reg,
		FeaturePlanes:           planes,
		FeatureLifecycles:       []lipplugin.Lifecycle{lc1, lc2},
		ReplaceCandidateSurface: true,
		Extensions: ExtensionsOptions{
			SecretGuardEnvironment: env,
			SecretDecisionObserver: obs,
			SecretGuardInputs: SecretGuardInputs{
				SingleUser: coresg.SingleUserOptions{
					IncludePopularEnv: true,
					IncludeEnv:        []string{"INC_A", "INC_B"},
					ExcludeEnv:        []string{"EXC_A", "EXC_B"},
					MinSecretBytes:    16,
					Matcher:           coresg.MatcherOptions{PreserveKnownPrefixes: true, MaskByte: '*'},
					MatcherConfigured: true,
				},
			},
		},
	}

	merged := mergeCandidateBuildOptions(nil, overlay)
	if merged == nil {
		t.Fatal("expected non-nil merged options")
	}
	if merged == overlay {
		t.Fatal("expected independent pointer, got shared pointer")
	}
	if merged.ReplaceCandidateSurface {
		t.Fatal("expected ReplaceCandidateSurface to be reset to false")
	}
	if merged.PluginRegistry != reg {
		t.Fatalf("expected PluginRegistry preserved from overlay, got %v", merged.PluginRegistry)
	}
	if merged.FeaturePlanes.IsZero() {
		t.Fatal("expected non-zero FeaturePlanes preserved from overlay")
	}
	if merged.Extensions.SecretGuardEnvironment != env {
		t.Fatalf("expected SecretGuardEnvironment interface identity preserved, got %v", merged.Extensions.SecretGuardEnvironment)
	}
	if merged.Extensions.SecretDecisionObserver != obs {
		t.Fatalf("expected SecretDecisionObserver interface identity preserved, got %v", merged.Extensions.SecretDecisionObserver)
	}

	// Two-way slice mutation isolation for FeatureLifecycles:
	merged.FeatureLifecycles[0] = dummyOptLifecycle{id: "mutated-merged-lc"}
	if overlay.FeatureLifecycles[0] != lc1 {
		t.Fatalf("mutating merged.FeatureLifecycles mutated overlay.FeatureLifecycles: got %v want %v", overlay.FeatureLifecycles[0], lc1)
	}
	overlay.FeatureLifecycles[1] = dummyOptLifecycle{id: "mutated-overlay-lc"}
	if merged.FeatureLifecycles[1] != lc2 {
		t.Fatalf("mutating overlay.FeatureLifecycles mutated merged.FeatureLifecycles: got %v want %v", merged.FeatureLifecycles[1], lc2)
	}

	// Two-way slice mutation isolation for IncludeEnv:
	merged.Extensions.SecretGuardInputs.SingleUser.IncludeEnv[0] = "MUTATED_INC_MERGED"
	if overlay.Extensions.SecretGuardInputs.SingleUser.IncludeEnv[0] != "INC_A" {
		t.Fatalf("mutating merged IncludeEnv mutated overlay: got %s", overlay.Extensions.SecretGuardInputs.SingleUser.IncludeEnv[0])
	}
	overlay.Extensions.SecretGuardInputs.SingleUser.IncludeEnv[1] = "MUTATED_INC_OVERLAY"
	if merged.Extensions.SecretGuardInputs.SingleUser.IncludeEnv[1] != "INC_B" {
		t.Fatalf("mutating overlay IncludeEnv mutated merged: got %s", merged.Extensions.SecretGuardInputs.SingleUser.IncludeEnv[1])
	}

	// Two-way slice mutation isolation for ExcludeEnv:
	merged.Extensions.SecretGuardInputs.SingleUser.ExcludeEnv[0] = "MUTATED_EXC_MERGED"
	if overlay.Extensions.SecretGuardInputs.SingleUser.ExcludeEnv[0] != "EXC_A" {
		t.Fatalf("mutating merged ExcludeEnv mutated overlay: got %s", overlay.Extensions.SecretGuardInputs.SingleUser.ExcludeEnv[0])
	}
	overlay.Extensions.SecretGuardInputs.SingleUser.ExcludeEnv[1] = "MUTATED_EXC_OVERLAY"
	if merged.Extensions.SecretGuardInputs.SingleUser.ExcludeEnv[1] != "EXC_B" {
		t.Fatalf("mutating overlay ExcludeEnv mutated merged: got %s", merged.Extensions.SecretGuardInputs.SingleUser.ExcludeEnv[1])
	}
}

func TestMergeCandidateBuildOptions_TwoWaySliceMutationIsolation_WithProcessAndOverlay(t *testing.T) {
	t.Parallel()

	t.Run("replace_true_clones_and_isolates_slices", func(t *testing.T) {
		t.Parallel()

		procLC := dummyOptLifecycle{id: "proc-lc"}
		candLC := dummyOptLifecycle{id: "cand-lc"}

		process := &BuildOptions{
			FeatureLifecycles: []lipplugin.Lifecycle{procLC},
			Extensions: ExtensionsOptions{
				SecretGuardInputs: SecretGuardInputs{
					SingleUser: coresg.SingleUserOptions{
						IncludeEnv: []string{"PROC_INC"},
						ExcludeEnv: []string{"PROC_EXC"},
					},
				},
			},
		}

		overlay := &BuildOptions{
			ReplaceCandidateSurface: true,
			FeatureLifecycles:       []lipplugin.Lifecycle{candLC},
			Extensions: ExtensionsOptions{
				SecretGuardInputs: SecretGuardInputs{
					SingleUser: coresg.SingleUserOptions{
						IncludeEnv: []string{"CAND_INC"},
						ExcludeEnv: []string{"CAND_EXC"},
					},
				},
			},
		}

		merged := mergeCandidateBuildOptions(process, overlay)
		if merged == process || merged == overlay {
			t.Fatal("expected independent pointer")
		}

		// Two-way lifecycle mutation
		merged.FeatureLifecycles[0] = dummyOptLifecycle{id: "mutated"}
		if overlay.FeatureLifecycles[0] != candLC || process.FeatureLifecycles[0] != procLC {
			t.Fatal("lifecycle mutation leaked")
		}
		overlay.FeatureLifecycles[0] = dummyOptLifecycle{id: "mutated-overlay"}
		if merged.FeatureLifecycles[0] == overlay.FeatureLifecycles[0] {
			t.Fatal("overlay lifecycle mutation leaked to merged")
		}

		// Two-way IncludeEnv / ExcludeEnv mutation
		merged.Extensions.SecretGuardInputs.SingleUser.IncludeEnv[0] = "MUTATED"
		if overlay.Extensions.SecretGuardInputs.SingleUser.IncludeEnv[0] != "CAND_INC" {
			t.Fatal("IncludeEnv mutation leaked to overlay")
		}
		overlay.Extensions.SecretGuardInputs.SingleUser.ExcludeEnv[0] = "MUTATED_OVERLAY"
		if merged.Extensions.SecretGuardInputs.SingleUser.ExcludeEnv[0] != "CAND_EXC" {
			t.Fatal("ExcludeEnv mutation leaked to merged")
		}
	})

	t.Run("nil_and_empty_slices_preserved_accurately", func(t *testing.T) {
		t.Parallel()

		process := &BuildOptions{
			Extensions: ExtensionsOptions{
				SecretGuardInputs: SecretGuardInputs{
					SingleUser: coresg.SingleUserOptions{
						IncludeEnv: nil,
						ExcludeEnv: []string{},
					},
				},
			},
		}
		overlay := &BuildOptions{
			ReplaceCandidateSurface: true,
			Extensions: ExtensionsOptions{
				SecretGuardInputs: SecretGuardInputs{
					SingleUser: coresg.SingleUserOptions{
						IncludeEnv: []string{},
						ExcludeEnv: nil,
					},
				},
			},
		}

		merged := mergeCandidateBuildOptions(process, overlay)
		if merged.Extensions.SecretGuardInputs.SingleUser.IncludeEnv == nil {
			t.Fatal("expected non-nil empty IncludeEnv")
		}
		if len(merged.Extensions.SecretGuardInputs.SingleUser.IncludeEnv) != 0 {
			t.Fatal("expected 0 length IncludeEnv")
		}
		if merged.Extensions.SecretGuardInputs.SingleUser.ExcludeEnv != nil {
			t.Fatal("expected nil ExcludeEnv")
		}
	})
}

func TestPrependGeneratedLifecycles_TableShapesAndTwoWayAliasing(t *testing.T) {
	t.Parallel()

	l1 := dummyOptLifecycle{id: "l1"}
	l2 := dummyOptLifecycle{id: "l2"}
	l3 := dummyOptLifecycle{id: "l3"}
	l4 := dummyOptLifecycle{id: "l4"}

	tests := []struct {
		name          string
		gen           []lipplugin.Lifecycle
		overlay       []lipplugin.Lifecycle
		wantNil       bool
		wantNonNilEmp bool
		wantElements  []lipplugin.Lifecycle
	}{
		{
			name:    "nil_nil_returns_nil",
			gen:     nil,
			overlay: nil,
			wantNil: true,
		},
		{
			name:          "nil_empty_returns_nonnil_empty",
			gen:           nil,
			overlay:       []lipplugin.Lifecycle{},
			wantNonNilEmp: true,
		},
		{
			name:          "empty_nil_returns_nonnil_empty",
			gen:           []lipplugin.Lifecycle{},
			overlay:       nil,
			wantNonNilEmp: true,
		},
		{
			name:          "empty_empty_returns_nonnil_empty",
			gen:           []lipplugin.Lifecycle{},
			overlay:       []lipplugin.Lifecycle{},
			wantNonNilEmp: true,
		},
		{
			name:         "nil_pop_returns_overlay",
			gen:          nil,
			overlay:      []lipplugin.Lifecycle{l1, l2},
			wantElements: []lipplugin.Lifecycle{l1, l2},
		},
		{
			name:         "pop_nil_returns_gen",
			gen:          []lipplugin.Lifecycle{l1, l2},
			overlay:      nil,
			wantElements: []lipplugin.Lifecycle{l1, l2},
		},
		{
			name:         "empty_pop_returns_overlay",
			gen:          []lipplugin.Lifecycle{},
			overlay:      []lipplugin.Lifecycle{l1, l2},
			wantElements: []lipplugin.Lifecycle{l1, l2},
		},
		{
			name:         "pop_empty_returns_gen",
			gen:          []lipplugin.Lifecycle{l1, l2},
			overlay:      []lipplugin.Lifecycle{},
			wantElements: []lipplugin.Lifecycle{l1, l2},
		},
		{
			name:         "pop_and_overlay_preserves_order",
			gen:          []lipplugin.Lifecycle{l1, l2},
			overlay:      []lipplugin.Lifecycle{l3, l4},
			wantElements: []lipplugin.Lifecycle{l1, l2, l3, l4},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Independent input clones and immutable pre-call expected snapshots
			genInput := slices.Clone(tc.gen)
			overlayInput := slices.Clone(tc.overlay)
			genSnapshot := slices.Clone(tc.gen)
			overlaySnapshot := slices.Clone(tc.overlay)

			result := prependGeneratedLifecycles(genInput, overlayInput)

			if tc.wantNil {
				if result != nil {
					t.Fatalf("expected nil result, got %+v", result)
				}
				return
			}

			if tc.wantNonNilEmp {
				if result == nil {
					t.Fatal("expected non-nil empty slice, got nil")
				}
				if len(result) != 0 {
					t.Fatalf("expected empty slice, got len %d", len(result))
				}
				return
			}

			if len(result) != len(tc.wantElements) {
				t.Fatalf("len mismatch: got %d want %d", len(result), len(tc.wantElements))
			}
			if !slices.Equal(result, tc.wantElements) {
				t.Fatalf("result elements mismatch: got %+v want %+v", result, tc.wantElements)
			}

			// Two-way aliasing checks:
			// 1. Mutate result[0] -> assert both genInput and overlayInput exactly equal their pre-call snapshots
			if len(result) > 0 {
				result[0] = dummyOptLifecycle{id: "mutated-result"}
				if !slices.Equal(genInput, genSnapshot) {
					t.Fatalf("mutating result[0] affected genInput: got %+v want %+v", genInput, genSnapshot)
				}
				if !slices.Equal(overlayInput, overlaySnapshot) {
					t.Fatalf("mutating result[0] affected overlayInput: got %+v want %+v", overlayInput, overlaySnapshot)
				}
				// Restore result[0] for subsequent input mutation checks
				result[0] = tc.wantElements[0]
			}

			// 2. Mutate genInput separately -> assert result stays expected
			if len(genInput) > 0 {
				genInput[0] = dummyOptLifecycle{id: "mutated-gen"}
				if !slices.Equal(result, tc.wantElements) {
					t.Fatalf("mutating genInput affected result: got %+v want %+v", result, tc.wantElements)
				}
				genInput[0] = genSnapshot[0]
			}

			// 3. Mutate overlayInput separately -> assert result stays expected
			if len(overlayInput) > 0 {
				overlayInput[0] = dummyOptLifecycle{id: "mutated-overlay"}
				if !slices.Equal(result, tc.wantElements) {
					t.Fatalf("mutating overlayInput affected result: got %+v want %+v", result, tc.wantElements)
				}
				overlayInput[0] = overlaySnapshot[0]
			}
		})
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

func TestMergeCandidateBuildOptions_LifecyclesAndExtensionsOverlay(t *testing.T) {
	t.Parallel()

	procLC := dummyOptLifecycle{id: "proc-lc"}
	process := &BuildOptions{
		FeatureLifecycles: []lipplugin.Lifecycle{procLC},
		Extensions: ExtensionsOptions{
			SecretGuardEnvironment: stubOptEnv{val: "v1"},
		},
	}

	candLC := dummyOptLifecycle{id: "cand-lc"}
	overlay := &BuildOptions{
		ReplaceCandidateSurface: false,
		FeatureLifecycles:       []lipplugin.Lifecycle{candLC},
		Extensions: ExtensionsOptions{
			SecretGuardEnvironment: stubOptEnv{val: "v2"},
		},
	}

	merged := mergeCandidateBuildOptions(process, overlay)
	if len(merged.FeatureLifecycles) != 1 || merged.FeatureLifecycles[0] != candLC {
		t.Fatalf("expected overlay FeatureLifecycles, got %+v", merged.FeatureLifecycles)
	}

	// Defensive copy: mutating merged.FeatureLifecycles must not mutate overlay.FeatureLifecycles
	merged.FeatureLifecycles[0] = dummyOptLifecycle{id: "mutated"}
	if overlay.FeatureLifecycles[0] != candLC {
		t.Fatalf("mutating merged mutated overlay.FeatureLifecycles")
	}
	if process.FeatureLifecycles[0] != procLC {
		t.Fatalf("mutating merged mutated process.FeatureLifecycles")
	}
}
