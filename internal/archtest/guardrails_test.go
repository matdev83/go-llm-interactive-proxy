package archtest

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Budgets are non-test line counts (approximate architectural mass).
// Raise only when intentionally growing a layer; see docs/architecture-guardrails.md.
var lineBudgets = []struct {
	dir string
	max int
}{
	{"internal/core", 32000},
	{"internal/pluginreg", 4500},
	{"internal/stdhttp", 3500},
	{"internal/infra/runtimebundle", 4500},
}

func TestLineComplexityBudgets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, b := range lineBudgets {
		b := b
		t.Run(b.dir, func(t *testing.T) {
			t.Parallel()
			n, err := countNonTestGoLines(filepath.Join(root, b.dir))
			if err != nil {
				t.Fatal(err)
			}
			if n > b.max {
				t.Fatalf("%s: %d non-test lines exceeds budget %d (see docs/architecture-guardrails.md)", b.dir, n, b.max)
			}
		})
	}
}

func TestStandardBundlePackagesHaveNoInitFunctions(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "pluginreg"),
		filepath.Join(root, "cmd", "lipstd"),
	}
	for _, dir := range dirs {
		dir := dir
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Parallel()
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
				if hasInitFunc(path) {
					t.Fatalf("forbid init() in standard bundle path (use explicit registration): %s", path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTestsMustNotRegisterStandardBundleInInit(t *testing.T) {
	t.Parallel()
	initDecl := "func init" + "("
	regStd := "RegisterStandard" + "Bundle()"
	root := repoRoot(t)
	var bad []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == "vendor" || base == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(b)
		if strings.Contains(s, initDecl) && strings.Contains(s, regStd) {
			bad = append(bad, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("forbid init-time standard bundle registration in tests; call RegisterStandardBundle from tests/helpers explicitly:\n%s", strings.Join(bad, "\n"))
	}
}

func countNonTestGoLines(dir string) (int, error) {
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
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			total++
		}
		closeErr := f.Close()
		if err := sc.Err(); err != nil {
			return err
		}
		return closeErr
	})
	return total, err
}

func hasInitFunc(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	s := string(b)
	// Naive but sufficient: registration must not hide in init().
	return strings.Contains(s, "func init(")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find go.mod above", wd)
	return ""
}
