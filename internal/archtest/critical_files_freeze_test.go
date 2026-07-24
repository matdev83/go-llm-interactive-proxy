package archtest

import (
	"path/filepath"
	"testing"
)

// migrationHotspotFreezeBaselineSHA is the reviewed production SHA whose
// measured physical line counts freeze the migration-critical files (Task 1.1).
const migrationHotspotFreezeBaselineSHA = "efe4624909cea318c7211d5cb3734059d3210802"

// expectedMigrationHotspotFreezes lists the Task 1.2 gravity wells that must
// appear in CriticalFileBudgets. BaselineMax is the immutable Task 1.1 measured
// ceiling at migrationHotspotFreezeBaselineSHA. CurrentMax is the present
// exact-measured ratchet (no growth headroom) and must equal CriticalFileBudgets.Max.
// FinalTarget is Requirement 11.3; LoweringTask is the contraction task that
// must ratchet CurrentMax downward.
var expectedMigrationHotspotFreezes = []struct {
	Path         string
	BaselineMax  int
	CurrentMax   int
	FinalTarget  int
	LoweringTask string
}{
	{
		Path:         "internal/infra/runtimehost/coordinator.go",
		BaselineMax:  797,
		CurrentMax:   359, // exact measured lines after Task 6.4 ReloadState extraction
		FinalTarget:  300,
		LoweringTask: "6.5",
	},
	{
		Path:         "internal/infra/runtimehost/generation.go",
		BaselineMax:  575,
		CurrentMax:   575,
		FinalTarget:  400,
		LoweringTask: "7.3",
	},
	{
		Path:         "internal/infra/runtimebundle/candidate_compile.go",
		BaselineMax:  440,
		CurrentMax:   393,
		FinalTarget:  350,
		LoweringTask: "4.2",
	},
	// internal/infra/runtimebundle/process_services.go freeze retired at Task
	// 5.5: CriticalFileBudgets now carries the exact post-contraction ceiling
	// (249, below the ≤300 final target) enforced by TestCriticalFileLineBudgets.
	{
		Path:         "pkg/lipruntime/build.go",
		BaselineMax:  367,
		CurrentMax:   321,
		FinalTarget:  150,
		LoweringTask: "5.2",
	},
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
			if n != want.CurrentMax {
				t.Fatalf("%s: measured %d lines, want exact CurrentMax ratchet %d (BaselineMax %d)", want.Path, n, want.CurrentMax, want.BaselineMax)
			}
			if criticalFileExceedsBudget(n, got.Max) {
				t.Fatalf("%s: current size %d must pass budget %d", want.Path, n, got.Max)
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
