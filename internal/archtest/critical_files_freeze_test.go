package archtest

import (
	"path/filepath"
	"testing"
)

// migrationHotspotFreezeBaselineSHA is the reviewed production SHA whose
// measured physical line counts freeze the migration-critical files (Task 1.1).
const migrationHotspotFreezeBaselineSHA = "efe4624909cea318c7211d5cb3734059d3210802"

// expectedMigrationHotspotFreezes lists the Task 1.2 gravity wells that must
// appear in CriticalFileBudgets at exact Task 1.1 measured ceilings (no growth
// headroom). FinalTarget is Requirement 11.3; LoweringTask is the contraction
// task that must ratchet the freeze downward.
var expectedMigrationHotspotFreezes = []struct {
	Path         string
	FreezeMax    int
	FinalTarget  int
	LoweringTask string
}{
	{
		Path:         "internal/infra/runtimehost/coordinator.go",
		FreezeMax:    797,
		FinalTarget:  300,
		LoweringTask: "6.5",
	},
	{
		Path:         "internal/infra/runtimehost/generation.go",
		FreezeMax:    575,
		FinalTarget:  400,
		LoweringTask: "7.3",
	},
	{
		Path:         "internal/infra/runtimebundle/candidate_compile.go",
		FreezeMax:    440,
		FinalTarget:  350,
		LoweringTask: "3.5",
	},
	{
		Path:         "internal/infra/runtimebundle/process_services.go",
		FreezeMax:    364,
		FinalTarget:  300,
		LoweringTask: "5.5",
	},
	{
		Path:         "pkg/lipruntime/build.go",
		FreezeMax:    367,
		FinalTarget:  150,
		LoweringTask: "8.1",
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
			got, ok := byPath[want.Path]
			if !ok {
				t.Fatalf("CriticalFileBudgets missing migration hotspot %s (baseline %s freeze %d; final target ≤%d via task %s)",
					want.Path, migrationHotspotFreezeBaselineSHA, want.FreezeMax, want.FinalTarget, want.LoweringTask)
			}
			if got.Max != want.FreezeMax {
				t.Fatalf("%s: freeze ceiling Max=%d, want exact Task 1.1 measured %d (baseline %s; no headroom)",
					want.Path, got.Max, want.FreezeMax, migrationHotspotFreezeBaselineSHA)
			}

			n, err := countFileLines(filepath.Join(root, want.Path))
			if err != nil {
				t.Fatalf("%s: %v", want.Path, err)
			}
			if n != want.FreezeMax {
				t.Fatalf("%s: measured %d lines, want exact freeze ceiling %d", want.Path, n, want.FreezeMax)
			}
			if criticalFileExceedsBudget(n, got.Max) {
				t.Fatalf("%s: current size %d must pass budget %d", want.Path, n, got.Max)
			}
			// Representative one-line growth must be rejected without mutating repo sources.
			if !criticalFileExceedsBudget(want.FreezeMax+1, got.Max) {
				t.Fatalf("%s: one-line growth (%d) must exceed freeze ceiling %d", want.Path, want.FreezeMax+1, got.Max)
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
