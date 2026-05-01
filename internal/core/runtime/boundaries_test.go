package runtime_test

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type goListPackage struct {
	ImportPath string
	Standard   bool
}

// depRule is a substring match against ImportPath with a dedicated failure line.
type depRule struct {
	Substr string
	ErrMsg string
}

// Core production packages must not import internal/plugins/...; integration tests that need
// concrete plugins may import them from *_test.go only (go list -deps -test=false excludes test
// files, so those imports are not in the production dependency graph). See also
// internal/archtest/ref_support_boundaries_test.go for composition-root reference-emulator rules.
func TestCorePackagesDoNotDependOnConcretePluginPackages(t *testing.T) {
	t.Parallel()

	assertDepsExcludeRules(t, []string{"./internal/core/..."}, []depRule{
		{"/internal/plugins/", "core package dependency leaked to concrete plugin package"},
		{"/internal/refclient", "core package dependency leaked to reference client emulator package"},
		{"/internal/refbackend", "core package dependency leaked to reference backend emulator package"},
	})
}

// TestProductionPackagesDoNotDependOnReferenceBackendEmulators ensures the
// reference backend emulator tree is not in the non-test dependency closure
// of production entrypoints (cmd, plugins, core, pkg, support packages).
// See task 10.0.7 (go-core-reimplementation-v1).
func TestProductionPackagesDoNotDependOnReferenceBackendEmulators(t *testing.T) {
	t.Parallel()

	roots := []string{
		"./cmd/...",
		"./internal/plugins/...",
		"./internal/core/...",
		"./internal/infra/...",
		"./internal/qa/...",
		"./pkg/...",
	}
	assertDepsExcludeRules(t, roots, []depRule{
		{"/internal/refbackend", "production package dependency leaked to reference backend emulator package"},
	})
}

// assertDepsExcludeRules runs `go list -deps -test=false -json` on the given
// patterns and fails if any non-standard package ImportPath matches a rule
// substring. Test dependencies are excluded so imports from *_test.go files
// do not affect the graph.
func assertDepsExcludeRules(t *testing.T, patterns []string, rules []depRule) {
	t.Helper()

	args := append([]string{"list", "-deps", "-test=false", "-json"}, patterns...)

	cmd := exec.Command("go", args...)
	cmd.Dir = repoRoot(t)

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list failed: %v", err)
	}

	decoder := json.NewDecoder(strings.NewReader(string(output)))
	for decoder.More() {
		var pkg goListPackage
		if err := decoder.Decode(&pkg); err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		if pkg.Standard {
			continue
		}
		for _, rule := range rules {
			if strings.Contains(pkg.ImportPath, rule.Substr) {
				t.Fatalf("%s: %s", rule.ErrMsg, pkg.ImportPath)
			}
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()

	cmd := exec.Command("go", "env", "GOMOD")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	gomod := strings.TrimSpace(string(output))
	if gomod == "" || gomod == osDevNull() {
		t.Fatal("go env GOMOD did not return a module path")
	}

	return filepath.Dir(gomod)
}

func osDevNull() string {
	return "NUL"
}
