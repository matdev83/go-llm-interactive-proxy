package archtest

import (
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
		relFile    string
		structName string
		// allowedExceptions maps fieldName -> reason
		allowedExceptions map[string]string
	}

	targets := []targetStruct{
		{
			relFile:    "internal/infra/runtimebundle/process_services_types.go",
			structName: "ProcessServices",
			allowedExceptions: map[string]string{
				"StandardFeatures": "Design-approved single standard-feature-host handle (Req 8.2, 13.4)",
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
				"CompactionDetector":   "Design-approved narrow consumer port interface (Req 13.4)",
				"TerminalPolicyReader": "Design-approved narrow consumer port interface (Req 13.4)",
				"InterleavedProcessor": "Design-approved narrow consumer port interface (Req 13.4)",
			},
		},
		{
			relFile:    "internal/core/runtime/executor_config.go",
			structName: "ExecutorConfig",
			allowedExceptions: map[string]string{
				"Interleaved": "Design-approved consumer port runtime group",
				"Compaction":  "Design-approved consumer port runtime group",
			},
		},
		{
			relFile:           "internal/infra/runtimebundle/options.go",
			structName:        "BuildOptions",
			allowedExceptions: map[string]string{},
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
				"SecretGuard":            "Core extension plane (extensions.SecretGuardPlane)",
				"SecretGuardInputs":      "Host capability input alias from featurehost",
				"SecretGuardEnvironment": "Host capability input alias from featurehost",
				"SecretDecisionObserver": "SDK observer contract",
				"SecretGuardInventory":   "Diagnostics inventory extras",
			},
		},
		{
			relFile:           "internal/infra/runtimehost/generation.go",
			structName:        "Generation",
			allowedExceptions: map[string]string{},
		},
	}

	// Forbidden per-feature substrings in field names or field types
	forbiddenFeatureSubstrings := []string{
		"Reasoning",
		"Keepwarm",
		"Compaction",
		"compactioncompose",
		"reasoningcompose",
		"secretguardcompose",
		"secretaudit",
		"reasoningreplay",
	}

	var violations []string

	for _, tgt := range targets {
		absPath := filepath.Join(root, filepath.FromSlash(tgt.relFile))
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, absPath, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", tgt.relFile, err)
		}

		var structType *ast.StructType
		ast.Inspect(node, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if ts.Name.Name == tgt.structName {
				if st, ok := ts.Type.(*ast.StructType); ok {
					structType = st
					return false
				}
			}
			return true
		})

		if structType == nil {
			t.Fatalf("could not find struct %s in %s", tgt.structName, tgt.relFile)
		}

		for _, field := range structType.Fields.List {
			for _, nameIdent := range field.Names {
				fieldName := nameIdent.Name
				if _, ok := tgt.allowedExceptions[fieldName]; ok {
					continue
				}

				// Check field name
				for _, sub := range forbiddenFeatureSubstrings {
					if strings.Contains(fieldName, sub) {
						violations = append(violations,
							tgt.structName+"."+fieldName+": field name contains forbidden feature token '"+sub+"'")
					}
				}

				// Check field type string
				typeStr := aggregateFieldTypeToString(field.Type)
				for _, sub := range forbiddenFeatureSubstrings {
					if strings.Contains(typeStr, sub) {
						violations = append(violations,
							tgt.structName+"."+fieldName+" ("+typeStr+"): field type contains forbidden feature token '"+sub+"'")
					}
				}
			}
		}
	}

	if len(violations) > 0 {
		t.Fatalf("generic aggregates contain forbidden per-feature fields (%d):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
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
