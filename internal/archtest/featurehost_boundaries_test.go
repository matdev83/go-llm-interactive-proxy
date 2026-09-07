package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
)

// TestFeatureHost_NoRequestPathResolver verifies featurehost exposes NO
// service locator, dynamic resolver, or request-time lookup methods (Req 8.4, Task 2.2).
func TestFeatureHost_NoRequestPathResolver(t *testing.T) {
	t.Parallel()

	rtType := reflect.TypeOf((*featurehost.Runtime)(nil))
	forbidden := []string{"Resolve", "Get", "Lookup", "Service", "Services", "GetService", "ResolveService"}

	for i := 0; i < rtType.NumMethod(); i++ {
		methodName := rtType.Method(i).Name
		for _, f := range forbidden {
			if strings.EqualFold(methodName, f) {
				t.Fatalf("featurehost.Runtime must not expose request-path resolver/service locator: %s", methodName)
			}
		}
	}
}

// TestFeatureHost_PrivacyFromRuntimeBundle verifies that runtimebundle does not
// access unexported fields or internal private subpackages of featurehost.
func TestFeatureHost_PrivacyFromRuntimeBundle(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	root := filepath.Join("..", "infra", "runtimebundle")

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}

		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(importPath, "github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost/") {
				t.Errorf("runtimebundle file %s illegally imports private featurehost subpackage: %s", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir failed: %v", err)
	}
}

// TestFeatureHost_CoreDoesNotImportFeatureHost verifies that core packages do not import featurehost.
func TestFeatureHost_CoreDoesNotImportFeatureHost(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	root := filepath.Join("..", "core")

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}

		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(importPath, "standardplugins/featurehost") {
				t.Errorf("core file %s illegally imports featurehost: %s", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir failed: %v", err)
	}
}

// TestFeatureHost_NoPreHandoffFeatureConstruction verifies that featurehost
// exposes no production-capable pre-handoff feature constructor inputs or
// test-injection APIs (Tasks 2.1/2.3, Requirements 2.5/8.4). Before the atomic
// Task 7.3 handoff, no external caller may construct, adopt, or inject
// feature-owned process resources or closers through the facade.
func TestFeatureHost_NoPreHandoffFeatureConstruction(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	root := filepath.Join("..", "standardplugins", "featurehost")

	forbiddenIdents := map[string]bool{
		"TerminalPolicyConfig":       true,
		"NewProcessWithStepsForTest": true,
		"ConstructionStep":           true,
		"RegisterCloserForTest":      true,
		"NewRuntimeForTest":          true,
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if forbiddenIdents[x.Name] {
					t.Errorf("featurehost file %s must not declare or reference pre-handoff injection API %s", path, x.Name)
				}
			case *ast.SelectorExpr:
				if pkg, ok := x.X.(*ast.Ident); ok && pkg.Name == "terminaldecisionpolicy" && x.Sel.Name == "NewStore" {
					t.Errorf("featurehost file %s must not construct terminaldecisionpolicy.Store before Task 7.3", path)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir failed: %v", err)
	}
}

// TestFeatureHost_NoConcreteFeatureAccessors verifies that featurehost does
// not expose concrete coordinator/parent-port/detector accessors outside its
// package: only the minimal detector consumer port in generation output and
// the transition-guard observers belong to the public surface (Task 3.3).
// Distinctness and counting checks live in package-local featurehost tests.
func TestFeatureHost_NoConcreteFeatureAccessors(t *testing.T) {
	t.Parallel()

	forbidden := []string{"BranchCoordinator", "CompactionParentPort"}
	fset := token.NewFileSet()
	root := filepath.Join("..", "standardplugins", "featurehost")

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				return true
			}
			for _, name := range forbidden {
				if fn.Name.Name == name {
					t.Errorf("featurehost file %s must not expose concrete accessor %s", path, name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir failed: %v", err)
	}
}

// TestFeatureHost_NoPackageLevelReasoningEntryPointsInRuntimeBundle verifies that runtimebundle prod
// code does not expose separate validation/binding paths alongside Runtime.CompileGeneration (R4).
func TestFeatureHost_NoPackageLevelReasoningEntryPointsInRuntimeBundle(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join("..", "infra", "runtimebundle", "reasoning_preservation_compression.go"), nil, 0)
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if fn.Name.Name == "validateReasoningPreservationCompressionGeneration" ||
			fn.Name.Name == "bindReasoningPreservationCompression" {
			t.Fatalf("runtimebundle prod code must not expose package-level reasoning entry point %s (R4)", fn.Name.Name)
		}
	}
}
