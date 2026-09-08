package runtimebundle

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

func TestRuntimeBundle_NoResidualSecretGuardConcreteImports(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly) //nolint:staticcheck // SA1019: intentional lightweight AST import scan of one package dir
	if err != nil {
		t.Fatalf("failed to parse runtimebundle package: %v", err)
	}

	const forbidden = "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard"

	var violatingFiles []string
	for _, pkg := range pkgs {
		for fileName, f := range pkg.Files {
			for _, imp := range f.Imports {
				importPath := strings.Trim(imp.Path.Value, `"`)
				if strings.HasPrefix(importPath, forbidden) {
					violatingFiles = append(violatingFiles, filepath.Base(fileName))
				}
			}
		}
	}

	if len(violatingFiles) > 0 {
		t.Fatalf("runtimebundle must have no residual secretguard feature imports, but found in: %v", violatingFiles)
	}
}

func TestRuntimeBundle_NoResidualSecretGuardHelpers(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0) //nolint:staticcheck // SA1019: intentional lightweight AST declaration scan of one package dir
	if err != nil {
		t.Fatalf("failed to parse runtimebundle package: %v", err)
	}
	for _, pkg := range pkgs {
		for fileName, f := range pkg.Files {
			for _, decl := range f.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok {
					if fn.Name.Name == "composeSecretGuardSingleUser" {
						t.Fatalf("runtimebundle must not contain residual helper %s in %s", fn.Name.Name, filepath.Base(fileName))
					}
				}
			}
		}
	}
}

type convergenceDummyEnv struct {
	lookups int
}

func (e *convergenceDummyEnv) Lookup(string) (string, bool) {
	e.lookups++
	return "", false
}

func (e *convergenceDummyEnv) Snapshot() []string {
	return nil
}

type convergenceDummyObserver struct{}

func (convergenceDummyObserver) OnSecretDecision(context.Context, sdk.DecisionEvent) error {
	return nil
}

func TestRuntimeBundle_SecretGuardCandidateOverlayAndReload(t *testing.T) {
	t.Parallel()

	baseSG := &extensions.SecretGuardPlane{AccessMode: "single_user"}
	baseObs := convergenceDummyObserver{}

	dst := ExtensionsOptions{
		SecretGuard:            baseSG,
		SecretDecisionObserver: baseObs,
	}

	candSG := &extensions.SecretGuardPlane{AccessMode: "multi_user"}
	candObs := convergenceDummyObserver{}
	src := ExtensionsOptions{
		SecretGuard:            candSG,
		SecretDecisionObserver: candObs,
	}

	overlayExtensions(&dst, src)

	// Candidate overlay: SecretGuard and SecretDecisionObserver are overridden if non-nil
	if dst.SecretGuard != candSG {
		t.Fatalf("expected SecretGuard to be candidate overlay plane")
	}
	if dst.SecretGuard == baseSG {
		t.Fatalf("expected base SecretGuard to be replaced")
	}
}
