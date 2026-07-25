package archtest

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// Task 9.2 final Requirement 11.3 ceilings for the five named migration hotspots.
const (
	processServicesHotspotPath           = "internal/infra/runtimebundle/process_services.go"
	processServicesExactCurrentRatchet   = 249
	processServicesFinalLineCeiling      = 300
	processServicesImmutableLoweringTask = "5.5"
)

// finalCriticalFileTarget is one Requirement 11.3 named hotspot enforced at the
// Task 9.2 post-contraction exact ratchet (physical lines == CriticalFileBudgets.Max
// == ExpectedExact) and at-or-under the immutable final ceiling.
type finalCriticalFileTarget struct {
	Path          string
	ExpectedExact int
	FinalCeiling  int
	LoweringTask  string
}

func finalCriticalFileTargets() []finalCriticalFileTarget {
	return []finalCriticalFileTarget{
		{
			Path:          coordinatorHotspotPath,
			ExpectedExact: coordinatorExactCurrentRatchet,
			FinalCeiling:  coordinatorFinalLineCeiling,
			LoweringTask:  coordinatorImmutableLoweringTask,
		},
		{
			Path:          generationHotspotPath,
			ExpectedExact: generationExactCurrentRatchet,
			FinalCeiling:  generationFinalLineCeiling,
			LoweringTask:  generationImmutableLoweringTask,
		},
		{
			Path:          candidateCompileHotspotPath,
			ExpectedExact: candidateCompileExactCurrentRatchet,
			FinalCeiling:  candidateCompileFinalLineCeiling,
			LoweringTask:  candidateCompileImmutableLoweringTask,
		},
		{
			Path:          processServicesHotspotPath,
			ExpectedExact: processServicesExactCurrentRatchet,
			FinalCeiling:  processServicesFinalLineCeiling,
			LoweringTask:  processServicesImmutableLoweringTask,
		},
		{
			Path:          lipruntimeBuildHotspotPath,
			ExpectedExact: lipruntimeBuildExactCurrentRatchet,
			FinalCeiling:  lipruntimeFinalLineCeiling,
			LoweringTask:  lipruntimeImmutableLoweringTask,
		},
	}
}

// packageBudgetIncrease records a prospective raise of a PackageTreeBudgets
// ceiling. Requirement 11.7: temporary raises must carry rationale plus an
// old-path deletion milestone; bare growth is rejected.
type packageBudgetIncrease struct {
	Tree              string
	OldMax            int
	NewMax            int
	Rationale         string
	DeletionMilestone string
}

// packageTreePostTask92Ratchets is the immutable Task 9.2 package-budget floor
// for future-increase review: the active exact budget may contract only when
// this ratchet contracts with it, and any growth above it must carry a waiver
// with rationale plus an old-path deletion milestone.
func packageTreePostTask92Ratchets() []PackageTreeBudget {
	return []PackageTreeBudget{
		{Tree: "internal/infra/runtimebundle", Max: 10418},
		{Tree: "internal/stdhttp", Max: 4509},
		{Tree: "cmd/lipstd", Max: 880},
		{Tree: "pkg/lipruntime", Max: 648},
	}
}

func packageTreeIncreaseWaivers() []packageBudgetIncrease { return nil }

func validatePackageBudgetIncrease(inc packageBudgetIncrease) error {
	if strings.TrimSpace(inc.Tree) == "" {
		return fmt.Errorf("package budget increase: Tree is required")
	}
	if inc.NewMax <= inc.OldMax {
		return fmt.Errorf("%s: NewMax %d is not an increase over OldMax %d", inc.Tree, inc.NewMax, inc.OldMax)
	}
	if strings.TrimSpace(inc.Rationale) == "" {
		return fmt.Errorf("%s: rationale required for package budget increase %d → %d", inc.Tree, inc.OldMax, inc.NewMax)
	}
	if strings.TrimSpace(inc.DeletionMilestone) == "" {
		return fmt.Errorf("%s: old-path deletion milestone required for package budget increase %d → %d", inc.Tree, inc.OldMax, inc.NewMax)
	}
	return nil
}

func TestValidatePackageBudgetIncrease(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      packageBudgetIncrease
		wantErr string
	}{
		{
			name: "rationale_and_deletion_milestone_pass",
			in: packageBudgetIncrease{
				Tree:              "internal/infra/runtimebundle",
				OldMax:            10418,
				NewMax:            10450,
				Rationale:         "absorb temporary dual-path during migration",
				DeletionMilestone: "Task 5.5",
			},
		},
		{
			name: "missing_rationale_fails",
			in: packageBudgetIncrease{
				Tree:              "internal/infra/runtimebundle",
				OldMax:            10418,
				NewMax:            10450,
				DeletionMilestone: "Task 5.5",
			},
			wantErr: "rationale required",
		},
		{
			name: "missing_deletion_milestone_fails",
			in: packageBudgetIncrease{
				Tree:      "internal/infra/runtimebundle",
				OldMax:    10418,
				NewMax:    10450,
				Rationale: "temporary dual-path",
			},
			wantErr: "old-path deletion milestone required",
		},
		{
			name: "non_increase_fails",
			in: packageBudgetIncrease{
				Tree:              "internal/infra/runtimebundle",
				OldMax:            10418,
				NewMax:            10418,
				Rationale:         "noop",
				DeletionMilestone: "Task 9.2",
			},
			wantErr: "is not an increase",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validatePackageBudgetIncrease(tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validatePackageBudgetIncrease: unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validatePackageBudgetIncrease: want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validatePackageBudgetIncrease: error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestRuntimeConvergencePackageTreeBudgetRatchets(t *testing.T) {
	t.Parallel()
	active := make(map[string]int, len(PackageTreeBudgets))
	for _, budget := range PackageTreeBudgets {
		active[budget.Tree] = budget.Max
	}
	ratchets := make(map[string]int, len(packageTreePostTask92Ratchets()))
	for _, ratchet := range packageTreePostTask92Ratchets() {
		ratchets[ratchet.Tree] = ratchet.Max
	}
	waivers := make(map[string]packageBudgetIncrease, len(packageTreeIncreaseWaivers()))
	for _, waiver := range packageTreeIncreaseWaivers() {
		if err := validatePackageBudgetIncrease(waiver); err != nil {
			t.Fatalf("invalid package-budget increase waiver for %s: %v", waiver.Tree, err)
		}
		waivers[waiver.Tree] = waiver
	}
	if len(active) != len(ratchets) {
		t.Fatalf("PackageTreeBudgets and Task 9.2 ratchets must cover the same trees: active=%d ratchets=%d", len(active), len(ratchets))
	}
	for tree, activeMax := range active {
		ratchetMax, ok := ratchets[tree]
		if !ok {
			t.Fatalf("%s: missing Task 9.2 package-tree ratchet", tree)
		}
		if activeMax < ratchetMax {
			t.Fatalf("%s: active budget %d is below Task 9.2 ratchet %d; lower packageTreePostTask92Ratchets with the measured contraction", tree, activeMax, ratchetMax)
		}
		if activeMax == ratchetMax {
			continue
		}
		waiver, ok := waivers[tree]
		if !ok {
			t.Fatalf("%s: budget increased from Task 9.2 ratchet %d to %d without rationale plus old-path deletion milestone", tree, ratchetMax, activeMax)
		}
		if waiver.OldMax != ratchetMax || waiver.NewMax != activeMax {
			t.Fatalf("%s: waiver old/new %d→%d must match ratchet/active %d→%d", tree, waiver.OldMax, waiver.NewMax, ratchetMax, activeMax)
		}
	}
	for tree := range ratchets {
		if _, ok := active[tree]; !ok {
			t.Fatalf("%s: Task 9.2 ratchet missing active PackageTreeBudgets entry", tree)
		}
	}
}

// TestRuntimeConvergenceFinalCriticalFileTargets enforces Requirement 11.3 final
// ceilings for all five named hotspots at the Task 9.2 exact current ratchet
// (no padded ceilings; one-line growth rejected).
func TestRuntimeConvergenceFinalCriticalFileTargets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	byPath := make(map[string]CriticalFileBudget, len(CriticalFileBudgets))
	for _, b := range CriticalFileBudgets {
		byPath[b.Path] = b
	}

	targets := finalCriticalFileTargets()
	if len(targets) != 5 {
		t.Fatalf("final critical targets: want 5 named hotspots, got %d", len(targets))
	}

	for _, want := range targets {
		t.Run(want.Path, func(t *testing.T) {
			t.Parallel()
			if want.LoweringTask == "" {
				t.Fatalf("%s: LoweringTask must name the contraction task", want.Path)
			}
			got, ok := byPath[want.Path]
			if !ok {
				t.Fatalf("CriticalFileBudgets missing final hotspot %s", want.Path)
			}
			n, err := countFileLines(filepath.Join(root, want.Path))
			if err != nil {
				t.Fatalf("%s: %v", want.Path, err)
			}

			// process_services freeze was retired at Task 5.5; enforce exact
			// budget+file equality and the final ceiling without freeze CurrentMax.
			if want.Path == processServicesHotspotPath {
				if got.Max != want.ExpectedExact {
					t.Fatalf("%s: CriticalFileBudgets.Max=%d, want exact %d", want.Path, got.Max, want.ExpectedExact)
				}
				if n != want.ExpectedExact {
					t.Fatalf("%s: measured %d, want exact %d", want.Path, n, want.ExpectedExact)
				}
				if n > want.FinalCeiling {
					t.Fatalf("%s: measured %d exceeds final ceiling %d", want.Path, n, want.FinalCeiling)
				}
			} else {
				if err := validateExactCurrentRatchet(exactCurrentRatchet{
					Path:             want.Path,
					ActualLines:      n,
					BudgetMax:        got.Max,
					FreezeCurrentMax: want.ExpectedExact,
					ExpectedExact:    want.ExpectedExact,
					FinalCeiling:     want.FinalCeiling,
				}); err != nil {
					t.Fatal(err)
				}
			}
			if !criticalFileExceedsBudget(want.ExpectedExact+1, got.Max) {
				t.Fatalf("%s: one-line growth (%d) must exceed exact ratchet %d", want.Path, want.ExpectedExact+1, want.ExpectedExact)
			}
		})
	}
}
