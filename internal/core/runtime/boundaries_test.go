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

func TestCorePackagesDoNotDependOnConcretePluginPackages(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("go", "list", "-deps", "-json", "./internal/core/...")
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

		if strings.Contains(pkg.ImportPath, "/internal/plugins/") {
			t.Fatalf("core package dependency leaked to concrete plugin package: %s", pkg.ImportPath)
		}
		if strings.Contains(pkg.ImportPath, "/internal/refclient") {
			t.Fatalf("core package dependency leaked to reference client emulator package: %s", pkg.ImportPath)
		}
		if strings.Contains(pkg.ImportPath, "/internal/refbackend") {
			t.Fatalf("core package dependency leaked to reference backend emulator package: %s", pkg.ImportPath)
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
