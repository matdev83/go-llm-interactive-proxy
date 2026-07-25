package archtest

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PackageTreeBudget caps the recursive non-test .go line count for a package
// tree (directory and subpackages). Values are also reported by make arch-report
// so advisory output and Task 4.4 exact-budget gates stay synchronized.
type PackageTreeBudget struct {
	Tree string // repo-relative directory using forward slashes
	Max  int
}

// PackageTreeBudgets is the single source of truth for runtime-convergence
// package-tree ceilings consumed by Task 4.4 exact-budget tests and
// make arch-report. Order is preserved for stable report output.
//
// Ratcheted in Task 4.2 / certified in Task 4.4 at measured post-deletion sizes
// with zero headroom (Requirement 11.7-11.8).
// Task 5.1: runtimebundle exact-measured after per-invocation loader parameter +
// unexported buildHost RED seam (no package-global loader hook).
// Task 5.2: exact-measured after BuildHost repair (shared hostBuildProbe seam +
// serve-only CLI gate flag); temporary raise recorded the pending Task 5.5
// deletion of the BuildBootstrap serve/attachment dual path (Req 11.7).
// Task 5.3: contracted to exact-measured 9991 after InspectRoutes/InspectInventory
// split and BootstrapInspect/App/feature-merge deletion (Req 11.7).
// Task 5.4: exact-measured 10200 for true unpublished ValidateDistribution plus
// omitSoleAlreadyClosed (mixed ErrAlreadyClosed cleanup preservation).
// Task 5.5: contracted to exact-measured 10020 after deleting BuildBootstrap/
// BootstrapResult/BootstrapMode/AttachReloadHost/LoadBootstrapEffective, moving
// installRegistryAndRegistrations/initProcessTracing/shutdownTracing into
// composition_root.go, and refactoring publishInitialGeneration to explicit
// multi-value returns (no BootstrapResult projection). The process_services.go
// / process_services_types.go split is a critical-file organization boundary
// only — it is neutral to this recursive package-tree total (Req 11.5-11.7).
// Task 5.5 also freezes cmd/lipstd at its exact measured post-deletion size.
// Task 7.2: raised to exact-measured 10163 after ResourceLedger sole phase
// ownership (retryable close state machine), Candidate transfer-vs-lifecycle
// exclusive claim, terminal start blocking, and deletion of Candidate/
// GenerationBundle/buildBackends duplicate lifecycle wrappers.
// Task 7.3: contracted to exact-measured 10162 after host_build.go dropped its
// direct runtimehost import (ShutdownDetached call sites simplified).
// Task 7.4: exact-measured after Host became the sole process shutdown
// coordinator with shared-attempt Close semantics, focused Host reload
// delegation, and consolidated pre-Host joinInitialFailureCleanup ownership.
// Attempt-3 repair removed the production closeAttemptWaitHook and resolved
// the HTTP handler once across validation/mount. runtimebundle absorbs the
// previously duplicated stdhttp/CLI orchestration; stdhttp loses
// shutdownGenerationHost/closeProcessServices and cmd/lipstd loses
// tracing_shutdown.go. Net non-test production delta vs Task 7.3 is
// +78 lines across the three trees.
// Task 8.1: runtimebundle exact-measured after Host facade query methods moved
// out of pkg/lipruntime (host_queries.go). pkg/lipruntime recursive non-test
// production ceiling is the b264155f pre-task total with no headroom after the
// public build/facade contraction (measured 819 ≤ 820).
// Task 8.2: runtimebundle drops deprecated ProductionOptions provider/rater
// fields and rejectUnboundLegacyAuthority (10419). pkg/lipruntime gains the
// explicit legacy_options quarantine adapter (+10 vs Task 8.1; measured 829).
// Task 8.4: delete legacy_options adapter and public deprecated Options fields
// (pkg/lipruntime measured 648). runtimebundle comment-only contraction (10418).
// Task 9.2: candidate_compile.go helpers moved to candidate_options.go (critical-
// file organization); recursive package-tree total unchanged at exact 10418.
// PR A: restore HostCapabilities queries + RollbackUnpublished + lifecycle observer wiring.
var PackageTreeBudgets = []PackageTreeBudget{
	{Tree: "internal/infra/runtimebundle", Max: 9468},
	{Tree: "internal/stdhttp", Max: 4301},
	{Tree: "cmd/lipstd", Max: 880},
	{Tree: "pkg/lipruntime", Max: 536},
}

// CountNonTestGoLines recursively counts physical lines in non-test .go files
// under dir (excludes *_test.go; includes subpackages). Semantics match the
// architecture guardrail tree budgets used by lineBudgets.
func CountNonTestGoLines(dir string) (int, error) {
	var total int
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		n, err := countTreeFileLines(path)
		if err != nil {
			return err
		}
		total += n
		return nil
	})
	return total, err
}

func countTreeFileLines(path string) (n int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		n++
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return n, nil
}

// FormatRuntimeConvergencePackageBudgets renders the advisory Markdown section
// that reports measured recursive non-test lines against PackageTreeBudgets.
func FormatRuntimeConvergencePackageBudgets(root string) (string, error) {
	var b strings.Builder
	fmt.Fprintln(&b, "## Runtime-convergence package budgets")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Tree | Non-test lines | Budget |")
	fmt.Fprintln(&b, "| --- | --- | --- |")
	for _, budget := range PackageTreeBudgets {
		n, err := CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(budget.Tree)))
		if err != nil {
			fmt.Fprintf(&b, "| `%s` | (missing: %v) | %d |\n", budget.Tree, err, budget.Max)
			continue
		}
		fmt.Fprintf(&b, "| `%s` | %d | %d |\n", budget.Tree, n, budget.Max)
	}
	fmt.Fprintln(&b)
	return b.String(), nil
}
