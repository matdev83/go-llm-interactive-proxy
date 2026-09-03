package runtimebundle

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/secretguardcompose"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

func TestRuntimeBundle_NoResidualSecretGuardConcreteImports(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
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
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
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

	baseEnv := &convergenceDummyEnv{}
	baseObs := convergenceDummyObserver{}
	baseInputs := SecretGuardInputs{
		SingleUser: secretguardcompose.SingleUserOptions{
			MinSecretBytes: 10,
		},
	}

	dst := ExtensionsOptions{
		SecretGuardEnvironment: baseEnv,
		SecretDecisionObserver: baseObs,
		SecretGuardInputs:      baseInputs,
	}

	candEnv := &convergenceDummyEnv{}
	candObs := convergenceDummyObserver{}
	src := ExtensionsOptions{
		SecretGuardEnvironment: candEnv,
		SecretDecisionObserver: candObs,
		SecretGuardInputs: SecretGuardInputs{
			SingleUser: secretguardcompose.SingleUserOptions{
				MinSecretBytes: 20,
			},
		},
	}

	overlayExtensions(&dst, src)

	// Candidate overlay: SecretGuardEnvironment and SecretDecisionObserver are overridden if non-nil
	if dst.SecretGuardEnvironment != candEnv {
		t.Fatalf("expected SecretGuardEnvironment to be candidate overlay env")
	}
	if dst.SecretGuardEnvironment == baseEnv {
		t.Fatalf("expected base SecretGuardEnvironment to be replaced")
	}
	// SecretGuardInputs is omitted from overlayExtensions and preserved from base
	if dst.SecretGuardInputs.SingleUser.MinSecretBytes != 10 {
		t.Fatalf("expected base SecretGuardInputs to be preserved, got min_secret_bytes=%d", dst.SecretGuardInputs.SingleUser.MinSecretBytes)
	}
}
