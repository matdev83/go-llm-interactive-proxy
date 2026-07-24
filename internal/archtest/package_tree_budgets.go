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
// serve-only CLI gate flag); temporary raise until Task 5.5 deletes remaining
// BuildBootstrap serve/attachment dual path (Req 11.7).
// Task 5.3: contracted to exact-measured 9991 after InspectRoutes/InspectInventory
// split and BootstrapInspect/App/feature-merge deletion (Req 11.7).
var PackageTreeBudgets = []PackageTreeBudget{
	{Tree: "internal/infra/runtimebundle", Max: 9991},
	{Tree: "internal/stdhttp", Max: 4522},
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
