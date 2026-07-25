package archtest

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// migrationHotspotFreezeBaselineSHA is the reviewed production SHA whose
// measured physical line counts freeze the migration-critical files (Task 1.1).
const migrationHotspotFreezeBaselineSHA = "efe4624909cea318c7211d5cb3734059d3210802"

// Task 6.5 Coordinator exact-current ratchet: active architecture metadata and
// the physical file must share one measured total (not a padded ≤300 ceiling).
const (
	coordinatorHotspotPath           = "internal/infra/runtimehost/coordinator.go"
	coordinatorExactCurrentRatchet   = 292
	coordinatorFinalLineCeiling      = 300
	coordinatorImmutableBaselineMax  = 797
	coordinatorImmutableLoweringTask = "6.5"
)

// Task 7.3 Generation exact-current ratchet: retirement scheduling moved to
// Manager/retire.go, Close/Discard moved to generation_close.go, payload
// binding moved to generation_payload.go, and refcount/drain moved to
// generation_refcount.go. The final ≤400 target (Req 11.3) is already beaten;
// active architecture metadata and the physical file share one measured total
// (not a padded ceiling).
const (
	generationHotspotPath           = "internal/infra/runtimehost/generation.go"
	generationExactCurrentRatchet   = 316
	generationFinalLineCeiling      = 400
	generationImmutableBaselineMax  = 575
	generationImmutableLoweringTask = "7.3"
)

// Task 8.1 public build/facade exact-current ratchet: Runtime retains one host
// seam; Build assembly lives in build.go at the measured post-split total.
const (
	lipruntimeBuildHotspotPath          = "pkg/lipruntime/build.go"
	lipruntimeBuildExactCurrentRatchet  = 96
	lipruntimeBuildImmutableBaselineMax = 367
	lipruntimeImmutableLoweringTask     = "8.1"
)

// Task 9.2 candidate compilation exact-current ratchet: mergeCandidateBuildOptions
// and hasExtensionOverlay moved to candidate_options.go so the named hotspot
// meets Requirement 11.3 (≤350). Active architecture metadata and the physical
// file share one measured total (not a padded ceiling).
const (
	candidateCompileHotspotPath           = "internal/infra/runtimebundle/candidate_compile.go"
	candidateCompileExactCurrentRatchet   = 323
	candidateCompileFinalLineCeiling      = 350
	candidateCompileImmutableBaselineMax  = 440
	candidateCompileImmutableLoweringTask = "9.2"
)

// expectedMigrationHotspotFreezes lists the Task 1.2 gravity wells that must
// appear in CriticalFileBudgets. BaselineMax is the immutable Task 1.1 measured
// ceiling at migrationHotspotFreezeBaselineSHA. CurrentMax is the present
// active ratchet and must equal CriticalFileBudgets.Max.
// FinalTarget is Requirement 11.3; LoweringTask is the contraction task that
// must ratchet CurrentMax downward.
//
// Completed lowering tasks require exact three-source equality (actual ==
// CriticalFileBudgets.Max == CurrentMax). Task 9.2 brings candidate compilation
// onto the same exact-current ratchet as coordinator/generation/lipruntime.
var expectedMigrationHotspotFreezes = []struct {
	Path         string
	BaselineMax  int
	CurrentMax   int
	FinalTarget  int
	LoweringTask string
}{
	{
		Path:         coordinatorHotspotPath,
		BaselineMax:  coordinatorImmutableBaselineMax,
		CurrentMax:   coordinatorExactCurrentRatchet, // exact measured lines after Task 6.5
		FinalTarget:  coordinatorFinalLineCeiling,
		LoweringTask: coordinatorImmutableLoweringTask,
	},
	{
		Path:         generationHotspotPath,
		BaselineMax:  generationImmutableBaselineMax,
		CurrentMax:   generationExactCurrentRatchet, // exact measured lines after Task 7.3
		FinalTarget:  generationFinalLineCeiling,
		LoweringTask: generationImmutableLoweringTask,
	},
	{
		Path:         candidateCompileHotspotPath,
		BaselineMax:  candidateCompileImmutableBaselineMax,
		CurrentMax:   candidateCompileExactCurrentRatchet, // exact measured lines after Task 9.2
		FinalTarget:  candidateCompileFinalLineCeiling,
		LoweringTask: candidateCompileImmutableLoweringTask,
	},
	// internal/infra/runtimebundle/process_services.go freeze retired at Task
	// 5.5: CriticalFileBudgets now carries the exact post-contraction ceiling
	// (249, below the ≤300 final target) enforced by TestCriticalFileLineBudgets
	// and TestRuntimeConvergenceFinalCriticalFileTargets.
	{
		Path:         "pkg/lipruntime/build.go",
		BaselineMax:  367,
		CurrentMax:   lipruntimeBuildExactCurrentRatchet, // exact measured lines after Task 8.1
		FinalTarget:  lipruntimeFinalLineCeiling,
		LoweringTask: lipruntimeImmutableLoweringTask,
	},
}

// exactCurrentRatchet equates three authoritative sources for a completed
// lowering task: physical lines, CriticalFileBudgets.Max, and freeze CurrentMax.
type exactCurrentRatchet struct {
	Path             string
	ActualLines      int
	BudgetMax        int
	FreezeCurrentMax int
	ExpectedExact    int
	FinalCeiling     int
}

// validateExactCurrentRatchet fails when any of the three current-ratchet
// sources disagree, when they drift from the accepted exact total, when the
// hard final ceiling is violated, or when metadata is stale/padded relative to
// the measured file. Silent shrink or padded CurrentMax must not pass.
func validateExactCurrentRatchet(r exactCurrentRatchet) error {
	if r.ExpectedExact <= 0 {
		return fmt.Errorf("%s: ExpectedExact must be positive", r.Path)
	}
	if r.FinalCeiling <= 0 {
		return fmt.Errorf("%s: FinalCeiling must be positive", r.Path)
	}
	if r.ExpectedExact > r.FinalCeiling {
		return fmt.Errorf("%s: ExpectedExact %d exceeds FinalCeiling %d", r.Path, r.ExpectedExact, r.FinalCeiling)
	}
	if r.BudgetMax != r.FreezeCurrentMax {
		return fmt.Errorf("%s: CriticalFileBudgets.Max=%d disagrees with freeze CurrentMax=%d", r.Path, r.BudgetMax, r.FreezeCurrentMax)
	}
	if r.BudgetMax != r.ExpectedExact {
		return fmt.Errorf("%s: CriticalFileBudgets.Max=%d, want exact current ratchet %d", r.Path, r.BudgetMax, r.ExpectedExact)
	}
	if r.FreezeCurrentMax != r.ExpectedExact {
		return fmt.Errorf("%s: freeze CurrentMax=%d, want exact current ratchet %d", r.Path, r.FreezeCurrentMax, r.ExpectedExact)
	}
	if r.ActualLines != r.ExpectedExact {
		switch {
		case r.ActualLines < r.ExpectedExact:
			return fmt.Errorf("%s: measured %d lines with stale/padded current ratchet %d (silent shrink must lower all current-ratchet metadata together)", r.Path, r.ActualLines, r.ExpectedExact)
		default:
			return fmt.Errorf("%s: measured %d lines exceed exact current ratchet %d", r.Path, r.ActualLines, r.ExpectedExact)
		}
	}
	if r.ActualLines > r.FinalCeiling {
		return fmt.Errorf("%s: measured %d lines exceed final ceiling %d", r.Path, r.ActualLines, r.FinalCeiling)
	}
	if r.FreezeCurrentMax > r.FinalCeiling {
		return fmt.Errorf("%s: freeze CurrentMax %d exceeds final ceiling %d", r.Path, r.FreezeCurrentMax, r.FinalCeiling)
	}
	return nil
}

func TestValidateExactCurrentRatchet(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      exactCurrentRatchet
		wantErr string
	}{
		{
			name: "actual_292_active_292_freeze_292_passes",
			in: exactCurrentRatchet{
				Path:             coordinatorHotspotPath,
				ActualLines:      292,
				BudgetMax:        292,
				FreezeCurrentMax: 292,
				ExpectedExact:    292,
				FinalCeiling:     300,
			},
		},
		{
			name: "simulated_actual_291_metadata_292_fails_stale_padded",
			in: exactCurrentRatchet{
				Path:             coordinatorHotspotPath,
				ActualLines:      291,
				BudgetMax:        292,
				FreezeCurrentMax: 292,
				ExpectedExact:    292,
				FinalCeiling:     300,
			},
			wantErr: "stale/padded",
		},
		{
			name: "simulated_actual_293_metadata_292_fails",
			in: exactCurrentRatchet{
				Path:             coordinatorHotspotPath,
				ActualLines:      293,
				BudgetMax:        292,
				FreezeCurrentMax: 292,
				ExpectedExact:    292,
				FinalCeiling:     300,
			},
			wantErr: "exceed exact current ratchet",
		},
		{
			name: "active_and_freeze_metadata_disagreement_fails",
			in: exactCurrentRatchet{
				Path:             coordinatorHotspotPath,
				ActualLines:      292,
				BudgetMax:        292,
				FreezeCurrentMax: 291,
				ExpectedExact:    292,
				FinalCeiling:     300,
			},
			wantErr: "disagrees with freeze CurrentMax",
		},
		{
			name: "budget_drift_from_expected_exact_fails",
			in: exactCurrentRatchet{
				Path:             coordinatorHotspotPath,
				ActualLines:      292,
				BudgetMax:        300,
				FreezeCurrentMax: 300,
				ExpectedExact:    292,
				FinalCeiling:     300,
			},
			wantErr: "want exact current ratchet 292",
		},
		{
			name: "final_ceiling_violation_fails",
			in: exactCurrentRatchet{
				Path:             coordinatorHotspotPath,
				ActualLines:      301,
				BudgetMax:        301,
				FreezeCurrentMax: 301,
				ExpectedExact:    301,
				FinalCeiling:     300,
			},
			wantErr: "exceeds FinalCeiling",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateExactCurrentRatchet(tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateExactCurrentRatchet: unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateExactCurrentRatchet: want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateExactCurrentRatchet: error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestCoordinatorTask65ExactCurrentRatchet(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	freezeIdx := -1
	for i, entry := range expectedMigrationHotspotFreezes {
		if entry.Path == coordinatorHotspotPath {
			freezeIdx = i
			break
		}
	}
	if freezeIdx < 0 {
		t.Fatalf("expectedMigrationHotspotFreezes missing %s", coordinatorHotspotPath)
	}
	freeze := expectedMigrationHotspotFreezes[freezeIdx]

	// Baseline / final / lowering metadata remain independently frozen from the
	// active exact-current ratchet (292).
	if freeze.BaselineMax != coordinatorImmutableBaselineMax {
		t.Fatalf("coordinator BaselineMax=%d, want immutable %d", freeze.BaselineMax, coordinatorImmutableBaselineMax)
	}
	if freeze.FinalTarget != coordinatorFinalLineCeiling {
		t.Fatalf("coordinator FinalTarget=%d, want immutable %d", freeze.FinalTarget, coordinatorFinalLineCeiling)
	}
	if freeze.LoweringTask != coordinatorImmutableLoweringTask {
		t.Fatalf("coordinator LoweringTask=%q, want immutable %q", freeze.LoweringTask, coordinatorImmutableLoweringTask)
	}
	if freeze.CurrentMax != coordinatorExactCurrentRatchet {
		t.Fatalf("coordinator CurrentMax=%d, want exact Task 6.5 ratchet %d", freeze.CurrentMax, coordinatorExactCurrentRatchet)
	}

	var budgetMax int
	foundBudget := false
	for _, b := range CriticalFileBudgets {
		if b.Path == coordinatorHotspotPath {
			budgetMax = b.Max
			foundBudget = true
			break
		}
	}
	if !foundBudget {
		t.Fatalf("CriticalFileBudgets missing %s", coordinatorHotspotPath)
	}

	n, err := countFileLines(filepath.Join(root, coordinatorHotspotPath))
	if err != nil {
		t.Fatalf("%s: %v", coordinatorHotspotPath, err)
	}

	if err := validateExactCurrentRatchet(exactCurrentRatchet{
		Path:             coordinatorHotspotPath,
		ActualLines:      n,
		BudgetMax:        budgetMax,
		FreezeCurrentMax: freeze.CurrentMax,
		ExpectedExact:    coordinatorExactCurrentRatchet,
		FinalCeiling:     coordinatorFinalLineCeiling,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLipruntimeBuildTask81ExactCurrentRatchet(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	freezeIdx := -1
	for i, entry := range expectedMigrationHotspotFreezes {
		if entry.Path == lipruntimeBuildHotspotPath {
			freezeIdx = i
			break
		}
	}
	if freezeIdx < 0 {
		t.Fatalf("expectedMigrationHotspotFreezes missing %s", lipruntimeBuildHotspotPath)
	}
	freeze := expectedMigrationHotspotFreezes[freezeIdx]

	if freeze.BaselineMax != lipruntimeBuildImmutableBaselineMax {
		t.Fatalf("lipruntime build BaselineMax=%d, want immutable %d", freeze.BaselineMax, lipruntimeBuildImmutableBaselineMax)
	}
	if freeze.FinalTarget != lipruntimeFinalLineCeiling {
		t.Fatalf("lipruntime build FinalTarget=%d, want immutable %d", freeze.FinalTarget, lipruntimeFinalLineCeiling)
	}
	if freeze.LoweringTask != lipruntimeImmutableLoweringTask {
		t.Fatalf("lipruntime build LoweringTask=%q, want immutable %q", freeze.LoweringTask, lipruntimeImmutableLoweringTask)
	}
	if freeze.CurrentMax != lipruntimeBuildExactCurrentRatchet {
		t.Fatalf("lipruntime build CurrentMax=%d, want exact Task 8.1 ratchet %d", freeze.CurrentMax, lipruntimeBuildExactCurrentRatchet)
	}

	var budgetMax int
	foundBudget := false
	for _, b := range CriticalFileBudgets {
		if b.Path == lipruntimeBuildHotspotPath {
			budgetMax = b.Max
			foundBudget = true
			break
		}
	}
	if !foundBudget {
		t.Fatalf("CriticalFileBudgets missing %s", lipruntimeBuildHotspotPath)
	}

	n, err := countFileLines(filepath.Join(root, lipruntimeBuildHotspotPath))
	if err != nil {
		t.Fatalf("%s: %v", lipruntimeBuildHotspotPath, err)
	}

	if err := validateExactCurrentRatchet(exactCurrentRatchet{
		Path:             lipruntimeBuildHotspotPath,
		ActualLines:      n,
		BudgetMax:        budgetMax,
		FreezeCurrentMax: freeze.CurrentMax,
		ExpectedExact:    lipruntimeBuildExactCurrentRatchet,
		FinalCeiling:     lipruntimeFinalLineCeiling,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGenerationTask73ExactCurrentRatchet(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	freezeIdx := -1
	for i, entry := range expectedMigrationHotspotFreezes {
		if entry.Path == generationHotspotPath {
			freezeIdx = i
			break
		}
	}
	if freezeIdx < 0 {
		t.Fatalf("expectedMigrationHotspotFreezes missing %s", generationHotspotPath)
	}
	freeze := expectedMigrationHotspotFreezes[freezeIdx]

	if freeze.BaselineMax != generationImmutableBaselineMax {
		t.Fatalf("generation BaselineMax=%d, want immutable %d", freeze.BaselineMax, generationImmutableBaselineMax)
	}
	if freeze.FinalTarget != generationFinalLineCeiling {
		t.Fatalf("generation FinalTarget=%d, want immutable %d", freeze.FinalTarget, generationFinalLineCeiling)
	}
	if freeze.LoweringTask != generationImmutableLoweringTask {
		t.Fatalf("generation LoweringTask=%q, want immutable %q", freeze.LoweringTask, generationImmutableLoweringTask)
	}
	if freeze.CurrentMax != generationExactCurrentRatchet {
		t.Fatalf("generation CurrentMax=%d, want exact Task 7.3 ratchet %d", freeze.CurrentMax, generationExactCurrentRatchet)
	}

	var budgetMax int
	foundBudget := false
	for _, b := range CriticalFileBudgets {
		if b.Path == generationHotspotPath {
			budgetMax = b.Max
			foundBudget = true
			break
		}
	}
	if !foundBudget {
		t.Fatalf("CriticalFileBudgets missing %s", generationHotspotPath)
	}

	n, err := countFileLines(filepath.Join(root, generationHotspotPath))
	if err != nil {
		t.Fatalf("%s: %v", generationHotspotPath, err)
	}

	if err := validateExactCurrentRatchet(exactCurrentRatchet{
		Path:             generationHotspotPath,
		ActualLines:      n,
		BudgetMax:        budgetMax,
		FreezeCurrentMax: freeze.CurrentMax,
		ExpectedExact:    generationExactCurrentRatchet,
		FinalCeiling:     generationFinalLineCeiling,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateCompileTask92ExactCurrentRatchet(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	freezeIdx := -1
	for i, entry := range expectedMigrationHotspotFreezes {
		if entry.Path == candidateCompileHotspotPath {
			freezeIdx = i
			break
		}
	}
	if freezeIdx < 0 {
		t.Fatalf("expectedMigrationHotspotFreezes missing %s", candidateCompileHotspotPath)
	}
	freeze := expectedMigrationHotspotFreezes[freezeIdx]

	if freeze.BaselineMax != candidateCompileImmutableBaselineMax {
		t.Fatalf("candidate compile BaselineMax=%d, want immutable %d", freeze.BaselineMax, candidateCompileImmutableBaselineMax)
	}
	if freeze.FinalTarget != candidateCompileFinalLineCeiling {
		t.Fatalf("candidate compile FinalTarget=%d, want immutable %d", freeze.FinalTarget, candidateCompileFinalLineCeiling)
	}
	if freeze.LoweringTask != candidateCompileImmutableLoweringTask {
		t.Fatalf("candidate compile LoweringTask=%q, want immutable %q", freeze.LoweringTask, candidateCompileImmutableLoweringTask)
	}
	if freeze.CurrentMax != candidateCompileExactCurrentRatchet {
		t.Fatalf("candidate compile CurrentMax=%d, want exact Task 9.2 ratchet %d", freeze.CurrentMax, candidateCompileExactCurrentRatchet)
	}

	var budgetMax int
	foundBudget := false
	for _, b := range CriticalFileBudgets {
		if b.Path == candidateCompileHotspotPath {
			budgetMax = b.Max
			foundBudget = true
			break
		}
	}
	if !foundBudget {
		t.Fatalf("CriticalFileBudgets missing %s", candidateCompileHotspotPath)
	}

	n, err := countFileLines(filepath.Join(root, candidateCompileHotspotPath))
	if err != nil {
		t.Fatalf("%s: %v", candidateCompileHotspotPath, err)
	}

	if err := validateExactCurrentRatchet(exactCurrentRatchet{
		Path:             candidateCompileHotspotPath,
		ActualLines:      n,
		BudgetMax:        budgetMax,
		FreezeCurrentMax: freeze.CurrentMax,
		ExpectedExact:    candidateCompileExactCurrentRatchet,
		FinalCeiling:     candidateCompileFinalLineCeiling,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCriticalFileMigrationHotspotFreezeBudgets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	byPath := make(map[string]CriticalFileBudget, len(CriticalFileBudgets))
	for _, b := range CriticalFileBudgets {
		byPath[b.Path] = b
	}

	for _, want := range expectedMigrationHotspotFreezes {
		t.Run(want.Path, func(t *testing.T) {
			t.Parallel()
			if want.CurrentMax > want.BaselineMax {
				t.Fatalf("%s: CurrentMax %d must be <= BaselineMax %d (baseline %s)",
					want.Path, want.CurrentMax, want.BaselineMax, migrationHotspotFreezeBaselineSHA)
			}
			got, ok := byPath[want.Path]
			if !ok {
				t.Fatalf("CriticalFileBudgets missing migration hotspot %s (baseline %s BaselineMax %d CurrentMax %d; final target ≤%d via task %s)",
					want.Path, migrationHotspotFreezeBaselineSHA, want.BaselineMax, want.CurrentMax, want.FinalTarget, want.LoweringTask)
			}
			if got.Max != want.CurrentMax {
				t.Fatalf("%s: CriticalFileBudgets.Max=%d, want CurrentMax ratchet %d (BaselineMax %d at %s; no headroom)",
					want.Path, got.Max, want.CurrentMax, want.BaselineMax, migrationHotspotFreezeBaselineSHA)
			}

			n, err := countFileLines(filepath.Join(root, want.Path))
			if err != nil {
				t.Fatalf("%s: %v", want.Path, err)
			}

			// Completed lowering tasks (coordinator/generation/candidate/
			// lipruntime): exact three-source equality via dedicated exact-
			// current ratchet validation. No remaining ceiling-only hotspots
			// in this freeze table after Task 9.2.
			switch want.Path {
			case coordinatorHotspotPath:
				if err := validateExactCurrentRatchet(exactCurrentRatchet{
					Path:             want.Path,
					ActualLines:      n,
					BudgetMax:        got.Max,
					FreezeCurrentMax: want.CurrentMax,
					ExpectedExact:    coordinatorExactCurrentRatchet,
					FinalCeiling:     want.FinalTarget,
				}); err != nil {
					t.Fatal(err)
				}
			case generationHotspotPath:
				if err := validateExactCurrentRatchet(exactCurrentRatchet{
					Path:             want.Path,
					ActualLines:      n,
					BudgetMax:        got.Max,
					FreezeCurrentMax: want.CurrentMax,
					ExpectedExact:    generationExactCurrentRatchet,
					FinalCeiling:     want.FinalTarget,
				}); err != nil {
					t.Fatal(err)
				}
			case candidateCompileHotspotPath:
				if err := validateExactCurrentRatchet(exactCurrentRatchet{
					Path:             want.Path,
					ActualLines:      n,
					BudgetMax:        got.Max,
					FreezeCurrentMax: want.CurrentMax,
					ExpectedExact:    candidateCompileExactCurrentRatchet,
					FinalCeiling:     want.FinalTarget,
				}); err != nil {
					t.Fatal(err)
				}
			case lipruntimeBuildHotspotPath:
				if err := validateExactCurrentRatchet(exactCurrentRatchet{
					Path:             want.Path,
					ActualLines:      n,
					BudgetMax:        got.Max,
					FreezeCurrentMax: want.CurrentMax,
					ExpectedExact:    lipruntimeBuildExactCurrentRatchet,
					FinalCeiling:     want.FinalTarget,
				}); err != nil {
					t.Fatal(err)
				}
			default:
				if criticalFileExceedsBudget(n, got.Max) {
					t.Fatalf("%s: current size %d must pass budget %d", want.Path, n, got.Max)
				}
			}

			// Representative one-line growth must be rejected without mutating repo sources.
			if !criticalFileExceedsBudget(want.CurrentMax+1, got.Max) {
				t.Fatalf("%s: one-line growth (%d) must exceed CurrentMax ratchet %d", want.Path, want.CurrentMax+1, want.CurrentMax)
			}
		})
	}
}

func TestCriticalFileExceedsBudgetBoundary(t *testing.T) {
	t.Parallel()
	if criticalFileExceedsBudget(100, 100) {
		t.Fatal("exact ceiling must not exceed budget")
	}
	if !criticalFileExceedsBudget(101, 100) {
		t.Fatal("one line over ceiling must exceed budget")
	}
}
