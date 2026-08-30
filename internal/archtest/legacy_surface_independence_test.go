package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestExtensionsFromMerged_SemanticIndependence characterizes Requirement 4.1, 4.4, 4.5 (Task 1.4):
// Proves compile_generation.go does not semantically derive extensions from legacy MergedFeatureSurface.
//
// If extensionsFromMerged has already been removed (post-Task 4.2 deletion), this test naturally passes (GREEN).
// While extensionsFromMerged exists, it identifies its legacy parameters from the signature and verifies
// via AST inspection that those parameter identifiers are NEVER referenced in the function body.
// This proves complete semantic independence without constructing obsolete types or calling obsolete signatures.
func TestExtensionsFromMerged_SemanticIndependence(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	compileGenPath := filepath.Join(root, "internal", "infra", "runtimebundle", "compile_generation.go")

	src, err := os.ReadFile(compileGenPath)
	require.NoError(t, err, "failed to read %s", compileGenPath)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, compileGenPath, src, 0)
	require.NoError(t, err, "failed to parse %s", compileGenPath)

	var extFn *ast.FuncDecl
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "extensionsFromMerged" {
			extFn = fn
			break
		}
	}

	// If extensionsFromMerged has been deleted in Task 4.2, the test remains green.
	if extFn == nil {
		t.Log("extensionsFromMerged does not exist in compile_generation.go (converged)")
		return
	}

	// Identify legacy parameters by inspecting parameter types in signature
	var legacyParamNames []string
	for _, field := range extFn.Type.Params.List {
		typeStr := astTypeNodeToString(field.Type)
		if strings.Contains(typeStr, "MergedFeatureSurface") {
			for _, nameIdent := range field.Names {
				legacyParamNames = append(legacyParamNames, nameIdent.Name)
			}
		}
	}

	require.NotEmpty(t, legacyParamNames, "extensionsFromMerged expected to have legacy MergedFeatureSurface parameter while it exists")

	// Inspect the function body: legacy parameter identifiers must NEVER be referenced
	if extFn.Body != nil {
		ast.Inspect(extFn.Body, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			for _, legacyName := range legacyParamNames {
				require.NotEqual(t, legacyName, ident.Name,
					"legacy parameter %q must not be referenced in extensionsFromMerged body (must have zero semantic influence)",
					legacyName)
			}
			return true
		})
	}
}

func astTypeNodeToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return fmt.Sprintf("%s.%s", astTypeNodeToString(t.X), t.Sel.Name)
	case *ast.StarExpr:
		return "*" + astTypeNodeToString(t.X)
	default:
		return ""
	}
}
