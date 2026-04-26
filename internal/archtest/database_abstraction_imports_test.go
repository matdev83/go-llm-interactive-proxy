package archtest

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

const bunModulePathPrefix = "github.com/uptrace/bun"

const infraDBImportPath = "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"

// allowedCoreBunDirectImporters are the only internal/core packages that may import
// github.com/uptrace/bun or its subpackages directly (bun-database-abstraction task 6.4).
var allowedCoreBunDirectImporters = map[string]struct{}{
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity/bunstore":             {},
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/bunstore": {},
}

// allowedCoreInfraDBDirectImporters are the only internal/core non-test packages that may
// import internal/infra/db (composition wiring to open managed stores).
var allowedCoreInfraDBDirectImporters = map[string]struct{}{
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity": {},
}

func importPathIsBunFamily(importPath string) bool {
	return importPath == bunModulePathPrefix || strings.HasPrefix(importPath, bunModulePathPrefix+"/")
}

// TestPublicPackagesDoNotTransitivelyImportBun ensures stable public contracts never pull
// Bun into their transitive dependency closure.
func TestPublicPackagesDoNotTransitivelyImportBun(t *testing.T) {
	t.Parallel()
	assertTransitiveDepsExcludeBun(t, []string{"./pkg/lipapi/...", "./pkg/lipsdk/..."})
}

// TestProtocolPluginsDoNotTransitivelyImportBun ensures official protocol plugins never pull
// Bun into their transitive dependency closure.
func TestProtocolPluginsDoNotTransitivelyImportBun(t *testing.T) {
	t.Parallel()
	patterns := []string{
		"./internal/plugins/frontends/...",
		"./internal/plugins/backends/...",
		"./internal/plugins/features/...",
	}
	assertTransitiveDepsExcludeBun(t, patterns)
}

// TestProtocolPluginsDoNotDirectlyImportBun ensures official protocol plugins never import
// Bun or the Bun pgdriver directly.
func TestProtocolPluginsDoNotDirectlyImportBun(t *testing.T) {
	t.Parallel()
	patterns := []string{
		"./internal/plugins/frontends/...",
		"./internal/plugins/backends/...",
		"./internal/plugins/features/...",
	}
	assertDirectImportsExcludeBunFamily(t, patterns)
}

// TestInternalCoreConfigDoesNotTransitivelyImportBun ensures core config stays free of Bun
// so validation and model packages do not pull ORM drivers transitively.
func TestInternalCoreConfigDoesNotTransitivelyImportBun(t *testing.T) {
	t.Parallel()
	assertTransitiveDepsExcludeBun(t, []string{"./internal/core/config/..."})
}

func assertTransitiveDepsExcludeBun(t *testing.T, patterns []string) {
	t.Helper()
	args := append([]string{"list", "-deps", "-test=false", "-json"}, patterns...)
	cmd := exec.Command("go", args...)
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if pkg.Standard {
			continue
		}
		if strings.Contains(pkg.ImportPath, bunModulePathPrefix) {
			t.Fatalf("package closure must not include Bun: found %s", pkg.ImportPath)
		}
	}
}

func assertDirectImportsExcludeBunFamily(t *testing.T, patterns []string) {
	t.Helper()
	args := append([]string{"list", "-json", "-test=false"}, patterns...)
	cmd := exec.Command("go", args...)
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if strings.HasSuffix(pkg.ImportPath, "_test") {
			continue
		}
		for _, imp := range pkg.Imports {
			if importPathIsBunFamily(imp) {
				t.Fatalf("package %q must not import %q directly", pkg.ImportPath, imp)
			}
		}
	}
}

// TestInternalCoreBunDirectImportsAreAllowlisted ensures only approved adapters import Bun
// directly from internal/core.
func TestInternalCoreBunDirectImportsAreAllowlisted(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("go", "list", "-json", "-test=false", "./internal/core/...")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if strings.HasSuffix(pkg.ImportPath, "_test") {
			continue
		}
		for _, imp := range pkg.Imports {
			if !importPathIsBunFamily(imp) {
				continue
			}
			if _, ok := allowedCoreBunDirectImporters[pkg.ImportPath]; !ok {
				t.Fatalf("internal/core package %q must not import %q directly (use approved adapter only)", pkg.ImportPath, imp)
			}
		}
	}
}

// TestInternalCoreInfraDBDirectImportsAreAllowlisted ensures internal/infra/db stays at
// composition roots, not in config or unrelated core packages.
func TestInternalCoreInfraDBDirectImportsAreAllowlisted(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("go", "list", "-json", "-test=false", "./internal/core/...")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if strings.HasSuffix(pkg.ImportPath, "_test") {
			continue
		}
		for _, imp := range pkg.Imports {
			if imp != infraDBImportPath {
				continue
			}
			if _, ok := allowedCoreInfraDBDirectImporters[pkg.ImportPath]; !ok {
				t.Fatalf("internal/core package %q must not import %q (move mapping to composition root)", pkg.ImportPath, imp)
			}
		}
	}
}

// TestInternalCoreConfigDoesNotImportInfraDBDirectly guards config against pulling database
// infrastructure for pool typing.
func TestInternalCoreConfigDoesNotImportInfraDBDirectly(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("go", "list", "-json", "-test=false", "./internal/core/config")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	if !dec.More() {
		t.Fatal("go list: empty output")
	}
	var pkg goListPackage
	if err := dec.Decode(&pkg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, imp := range pkg.Imports {
		if imp == infraDBImportPath {
			t.Fatalf("internal/core/config must not import %q", imp)
		}
		if importPathIsBunFamily(imp) {
			t.Fatalf("internal/core/config must not import %q", imp)
		}
	}
}

// TestContinuityFactoryDoesNotImportBunDirectly reinforces that the continuity composition
// factory wires Bun only through bunstore, not by importing ORM symbols at the factory package.
func TestContinuityFactoryDoesNotImportBunDirectly(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("go", "list", "-json", "-test=false", "./internal/core/continuity")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	if !dec.More() {
		t.Fatal("go list: empty output")
	}
	var pkg goListPackage
	if err := dec.Decode(&pkg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, imp := range pkg.Imports {
		if importPathIsBunFamily(imp) {
			t.Fatalf("internal/core/continuity must not import %q directly", imp)
		}
	}
}
