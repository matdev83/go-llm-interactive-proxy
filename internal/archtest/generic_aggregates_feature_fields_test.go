package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenericAggregatesContainNoPerFeatureFields enforces Requirement 13.4 and Task 10.4:
// generic aggregates (ProcessServices, ProcessServicesInput, executorBuildInput, ExecutorConfig,
// BuildOptions, ProductionOptions, TestingOptions, ExtensionsOptions, runtimehost.Generation)
// must contain zero fields named or typed after concrete standard feature IDs or packages,
// except the single StandardFeatures *featurehost.Runtime handle and design-approved consumer ports.
func TestGenericAggregatesContainNoPerFeatureFields(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	type targetStruct struct {
		relFile           string
		structName        string
		allowedExceptions map[string]string
	}

	targets := []targetStruct{
		{
			relFile:    "internal/infra/runtimebundle/process_services_types.go",
			structName: "ProcessServices",
			allowedExceptions: map[string]string{
				"StandardFeatures": "*featurehost.Runtime",
			},
		},
		{
			relFile:           "internal/infra/runtimebundle/process_services_types.go",
			structName:        "ProcessServicesInput",
			allowedExceptions: map[string]string{},
		},
		{
			relFile:    "internal/infra/runtimebundle/build_executor.go",
			structName: "executorBuildInput",
			allowedExceptions: map[string]string{
				"CompactionDetector":   "runtime.CompactionDetector",
				"TerminalPolicyReader": "runtime.TerminalPolicyReader",
				"InterleavedProcessor": "runtime.InterleavedProcessor",
			},
		},
		{
			relFile:    "internal/core/runtime/executor_config.go",
			structName: "ExecutorConfig",
			allowedExceptions: map[string]string{
				"Interleaved":                "InterleavedRuntime",
				"Compaction":                 "CompactionRuntime",
				"Processor":                  "InterleavedProcessor",
				"Detector":                   "CompactionDetector",
				"SecretGuardDecisionMetrics": "extensions.SecretGuardDecisionMetrics",
				"ConversationViewObserver":   "ConversationViewObserver",
				"TerminalPolicyReader":       "TerminalPolicyReader",
			},
		},
		{
			relFile:    "internal/infra/runtimebundle/options.go",
			structName: "BuildOptions",
			allowedExceptions: map[string]string{
				"SecretGuard":            "*extensions.SecretGuardPlane",
				"SecretGuardInventory":   "*diag.InventoryExtras",
				"SecretDecisionObserver": "sdk.Observer",
			},
		},
		{
			relFile:           "internal/infra/runtimebundle/production_options.go",
			structName:        "ProductionOptions",
			allowedExceptions: map[string]string{},
		},
		{
			relFile:           "internal/infra/runtimebundle/options.go",
			structName:        "TestingOptions",
			allowedExceptions: map[string]string{},
		},
		{
			relFile:    "internal/infra/runtimebundle/options.go",
			structName: "ExtensionsOptions",
			allowedExceptions: map[string]string{
				"SecretGuard":            "*extensions.SecretGuardPlane",
				"SecretGuardInventory":   "*diag.InventoryExtras",
				"SecretDecisionObserver": "sdk.Observer",
			},
		},
		{
			relFile:           "internal/infra/runtimehost/generation.go",
			structName:        "Generation",
			allowedExceptions: map[string]string{},
		},
	}

	var violations []string

	for _, tgt := range targets {
		absPath := filepath.Join(root, filepath.FromSlash(tgt.relFile))
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, absPath, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", tgt.relFile, err)
		}

		structViolations := scanStructForFeatureFields(node, tgt.structName, tgt.allowedExceptions)
		violations = append(violations, structViolations...)
	}

	if len(violations) > 0 {
		t.Fatalf("generic aggregates contain forbidden per-feature fields (%d):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

func TestGenericAggregates_NegativeFixtures(t *testing.T) {
	t.Parallel()
	const fixtureSrc = `package fixture
import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/keepwarm"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost/sessionpolicy"
)

type SyntheticViolationAggregate struct {
	Policy *sessionpolicy.Store
	Manager *keepwarm.Manager
}

type SyntheticEmbeddedAggregate struct {
	*keepwarm.Manager
}
`
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "synthetic.go", fixtureSrc, 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	violations := scanStructForFeatureFields(node, "SyntheticViolationAggregate", nil)
	if len(violations) < 2 {
		t.Fatalf("expected at least 2 violations for Policy *sessionpolicy.Store and Manager *keepwarm.Manager, got %d:\n%s",
			len(violations), strings.Join(violations, "\n"))
	}

	embeddedViolations := scanStructForFeatureFields(node, "SyntheticEmbeddedAggregate", nil)
	if len(embeddedViolations) < 1 {
		t.Fatalf("expected at least 1 violation for embedded *keepwarm.Manager, got %d", len(embeddedViolations))
	}
}

func scanStructForFeatureFields(node *ast.File, structName string, allowedExceptions map[string]string) []string {
	// 1. Build package import map: localPkgName -> fullImportPath
	importMap := make(map[string]string)
	for _, imp := range node.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		localName := filepath.Base(importPath)
		if imp.Name != nil {
			localName = imp.Name.Name
		}
		importMap[localName] = importPath
	}

	// 2. Collect all struct types declared in this file for recursive inspection
	fileStructs := make(map[string]*ast.StructType)
	ast.Inspect(node, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		if st, ok := ts.Type.(*ast.StructType); ok {
			fileStructs[ts.Name.Name] = st
		}
		return true
	})

	rootST, exists := fileStructs[structName]
	if !exists {
		return nil
	}

	forbiddenFeatureTokens := []string{
		"reasoning",
		"keepwarm",
		"compaction",
		"secretguard",
		"interleavedthinking",
		"interleaved",
		"sessionpolicy",
	}

	isForbiddenPkg := func(importPath string) bool {
		if strings.HasPrefix(importPath, "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features") {
			return true
		}
		if strings.HasPrefix(importPath, "github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost/") {
			return true
		}
		if strings.Contains(importPath, "secretguardcompose") ||
			strings.Contains(importPath, "compactioncompose") ||
			strings.Contains(importPath, "reasoningcompose") ||
			strings.Contains(importPath, "reasoningreplay") {
			return true
		}
		return false
	}

	checkTypeForForbiddenPkg := func(expr ast.Expr) (string, bool) {
		var forbiddenPkg string
		var found bool
		ast.Inspect(expr, func(n ast.Node) bool {
			if found {
				return false
			}
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok {
					if impPath, ok := importMap[ident.Name]; ok && isForbiddenPkg(impPath) {
						forbiddenPkg = impPath
						found = true
						return false
					}
				}
			}
			return true
		})
		return forbiddenPkg, found
	}

	unwrapTypeName := func(expr ast.Expr) string {
		curr := expr
		for {
			switch t := curr.(type) {
			case *ast.StarExpr:
				curr = t.X
			case *ast.ArrayType:
				curr = t.Elt
			case *ast.Ident:
				return t.Name
			default:
				return ""
			}
		}
	}

	var violations []string
	visited := make(map[string]bool)

	var scanStruct func(currName string, st *ast.StructType)
	scanStruct = func(currName string, st *ast.StructType) {
		if visited[currName] {
			return
		}
		visited[currName] = true

		for _, field := range st.Fields.List {
			typeStr := aggregateFieldTypeToString(field.Type)
			fieldNames := field.Names
			isEmbedded := len(fieldNames) == 0

			namesToCheck := make([]string, 0, len(fieldNames))
			if isEmbedded {
				derived := typeStr
				if star, ok := field.Type.(*ast.StarExpr); ok {
					derived = aggregateFieldTypeToString(star.X)
				}
				if sel, ok := field.Type.(*ast.SelectorExpr); ok {
					derived = sel.Sel.Name
				} else if star, ok := field.Type.(*ast.StarExpr); ok {
					if sel, ok := star.X.(*ast.SelectorExpr); ok {
						derived = sel.Sel.Name
					}
				}
				namesToCheck = append(namesToCheck, derived)
			} else {
				for _, n := range fieldNames {
					namesToCheck = append(namesToCheck, n.Name)
				}
			}

			for _, fieldName := range namesToCheck {
				qualName := currName + "." + fieldName

				expectedType := ""
				isAllowed := false
				if exp, ok := allowedExceptions[qualName]; ok {
					expectedType = exp
					isAllowed = true
				} else if exp, ok := allowedExceptions[fieldName]; ok {
					expectedType = exp
					isAllowed = true
				}

				if isAllowed {
					if typeStr != expectedType {
						violations = append(violations,
							fmt.Sprintf("%s (%s): field type does not match approved type %q",
								qualName, typeStr, expectedType))
					}
					innerType := unwrapTypeName(field.Type)
					if nestedST, ok := fileStructs[innerType]; ok && !visited[innerType] {
						scanStruct(innerType, nestedST)
					}
					continue
				}

				// Check 1: Does type reference a forbidden package?
				if forbiddenPkg, found := checkTypeForForbiddenPkg(field.Type); found {
					label := qualName
					if isEmbedded {
						label = fmt.Sprintf("%s.[embedded %s]", currName, typeStr)
					}
					violations = append(violations,
						fmt.Sprintf("%s (%s): field type imports forbidden feature package %q",
							label, typeStr, forbiddenPkg))
					continue
				}

				// Check 2: Does field name contain a forbidden token (case-insensitive)?
				lowerFieldName := strings.ToLower(fieldName)
				for _, token := range forbiddenFeatureTokens {
					if strings.Contains(lowerFieldName, token) {
						label := qualName
						if isEmbedded {
							label = fmt.Sprintf("%s.[embedded %s]", currName, typeStr)
						}
						violations = append(violations,
							fmt.Sprintf("%s: field name contains forbidden feature token %q",
								label, token))
						break
					}
				}

				// Check 3: Does type string contain a forbidden token (case-insensitive)?
				lowerTypeStr := strings.ToLower(typeStr)
				for _, token := range forbiddenFeatureTokens {
					if strings.Contains(lowerTypeStr, token) {
						label := qualName
						if isEmbedded {
							label = fmt.Sprintf("%s.[embedded %s]", currName, typeStr)
						}
						violations = append(violations,
							fmt.Sprintf("%s (%s): field type contains forbidden feature token %q",
								label, typeStr, token))
						break
					}
				}

				// If the field type is a nested struct in the same file, recurse into it
				innerType := unwrapTypeName(field.Type)
				if nestedST, ok := fileStructs[innerType]; ok && !visited[innerType] {
					scanStruct(innerType, nestedST)
				}
			}
		}
	}

	scanStruct(structName, rootST)
	return violations
}

func aggregateFieldTypeToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return aggregateFieldTypeToString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + aggregateFieldTypeToString(t.X)
	case *ast.ArrayType:
		return "[]" + aggregateFieldTypeToString(t.Elt)
	case *ast.MapType:
		return "map[" + aggregateFieldTypeToString(t.Key) + "]" + aggregateFieldTypeToString(t.Value)
	default:
		return ""
	}
}
