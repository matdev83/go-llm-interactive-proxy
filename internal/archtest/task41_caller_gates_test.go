package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestTask41_ProductionNoCompatibilityBuildCall(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateTask41BuildCall, scanTask41BuildCallSource)
	if len(got) > 0 {
		t.Fatalf("Task 4.1 RED: production must not call compatibility runtimebundle.Build (%d findings):\n%s",
			len(got), formatFindings(got))
	}
}

func TestTask41_ProductionNoBuiltCarrierOutsideScheduled(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateTask41BuiltCarrier, scanTask41BuiltCarrierSource)
	if len(got) > 0 {
		t.Fatalf("Task 4.1: production must not carry runtimebundle.Built outside scheduled declaration sites (%d findings):\n%s",
			len(got), formatFindings(got))
	}
}

func TestTask41_TestsNoLegacyBuildBuiltCallers(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanTestConvergenceGate(t, root, gateTask41TestLegacyCaller, scanTask41TestLegacyCallerSource)
	if len(got) > 0 {
		t.Fatalf("Task 4.1 RED: non-detector tests must not call Build/Built/NewStandardHandler/RunWithRuntime (%d findings):\n%s",
			len(got), formatFindings(got))
	}
}

func TestTask41_NoReplacementAggregateHelpers(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanTestConvergenceGate(t, root, gateTask41ReplacementAggregate, scanTask41ReplacementAggregateSource)
	if len(got) > 0 {
		t.Fatalf("Task 4.1: tests must not introduce Built-mirroring replacement aggregates (%d findings):\n%s",
			len(got), formatFindings(got))
	}
}

func TestTask41_NoLifecycleComposeHelpers(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanTestConvergenceGate(t, root, gateTask41LifecycleComposeHelper, scanTask41LifecycleComposeHelperSource)
	if len(got) > 0 {
		t.Fatalf("Task 4.1: test helpers must not combine HTTP composition with App Start/Shutdown (%d findings):\n%s",
			len(got), formatFindings(got))
	}
}

func formatFindings(fs []convergenceFinding) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(f.String())
		b.WriteByte('\n')
	}
	return b.String()
}

func scanTestConvergenceGate(t *testing.T, root, gate string, scan convergenceScanner) []convergenceFinding {
	t.Helper()
	var out []convergenceFinding
	err := walkTestGoFiles(root, func(rel, abs string, src []byte) error {
		rel = filepath.ToSlash(rel)
		fs, err := scan(rel, string(src))
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		for _, f := range fs {
			if f.Gate != gate {
				continue
			}
			f.Path = rel
			out = append(out, f)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk tests: %v", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

func walkTestGoFiles(root string, fn func(rel, abs string, src []byte) error) error {
	for _, top := range productionScanRoots() {
		base := filepath.Join(root, top)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				name := info.Name()
				if name == "vendor" || name == "testdata" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return fn(rel, path, src)
		})
		if err != nil {
			return err
		}
	}
	return nil
}
