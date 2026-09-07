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
		relFile           string
		structName        string
		allowedExceptions map[string]string
	}

	targets := []targetStruct{
		{
			relFile:    "internal/infra/runtimebundle/process_services_types.go",
			structName: "ProcessServices",
			allowedExceptions: map[string]string{
				"StandardFeatures": "*" + archTestModulePath + "/internal/standardplugins/featurehost.Runtime",
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
				"CompactionDetector":   archTestModulePath + "/internal/core/runtime.CompactionDetector",
				"TerminalPolicyReader": archTestModulePath + "/internal/core/runtime.TerminalPolicyReader",
				"InterleavedProcessor": archTestModulePath + "/internal/core/runtime.InterleavedProcessor",
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
				"SecretGuardDecisionMetrics": archTestModulePath + "/internal/core/extensions.SecretGuardDecisionMetrics",
				"ConversationViewObserver":   "ConversationViewObserver",
				"TerminalPolicyReader":       "TerminalPolicyReader",
			},
		},
		{
			relFile:    "internal/infra/runtimebundle/options.go",
			structName: "BuildOptions",
			allowedExceptions: map[string]string{
				"SecretGuard":            "*" + archTestModulePath + "/internal/core/extensions.SecretGuardPlane",
				"SecretGuardInventory":   "*" + archTestModulePath + "/internal/core/diag.InventoryExtras",
				"SecretDecisionObserver": archTestModulePath + "/pkg/lipsdk/secretguard.Observer",
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
				"SecretGuard":            "*" + archTestModulePath + "/internal/core/extensions.SecretGuardPlane",
				"SecretGuardInventory":   "*" + archTestModulePath + "/internal/core/diag.InventoryExtras",
				"SecretDecisionObserver": archTestModulePath + "/pkg/lipsdk/secretguard.Observer",
			},
		},
		{
			relFile:           "internal/infra/runtimehost/generation.go",
			structName:        "Generation",
			allowedExceptions: map[string]string{},
		},
	}

	var violations []string

	scopes := make(map[string]*archPkgScope)
	dirRows := make(map[string]map[string]map[string]string)
	for _, tgt := range targets {
		absPath := filepath.Join(root, filepath.FromSlash(tgt.relFile))
		dir := filepath.Dir(absPath)
		scope, ok := scopes[dir]
		if !ok {
			scope = archParseDir(t, dir)
			scopes[dir] = scope
		}
		rows, ok := dirRows[dir]
		if !ok {
			rows = make(map[string]map[string]string)
			dirRows[dir] = rows
		}
		rows[tgt.structName] = tgt.allowedExceptions
	}
	for _, tgt := range targets {
		absPath := filepath.Join(root, filepath.FromSlash(tgt.relFile))
		dir := filepath.Dir(absPath)
		structViolations := scopes[dir].scanWithRows(tgt.structName, dirRows[dir])
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

func TestGenericAggregates_NegativeFixtures_RatchetBypasses(t *testing.T) {
	t.Parallel()
	realSGPlane := "*" + archTestModulePath + "/internal/core/extensions.SecretGuardPlane"
	cases := []struct {
		name       string
		sources    map[string]string
		target     string
		exceptions map[string]string
		wantCount  int // minimum violations when positive, exact zero when negative
	}{
		{
			name: "alias indirection hides forbidden package",
			sources: map[string]string{"synthetic.go": `package fixture
import "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/keepwarm"
type ManagerAlias = keepwarm.Manager
type ManagerAliasChain = ManagerAlias
type AliasAggregate struct {
	Mgr ManagerAlias
	MgrChain ManagerAliasChain
}
`},
			target:     "AliasAggregate",
			exceptions: nil,
			wantCount:  2,
		},
		{
			name: "cross-file nested group escapes single-file recursion",
			sources: map[string]string{
				"outer.go": `package fixture
type OuterAggregate struct {
	Inner NestedGroup
}
`,
				"inner.go": `package fixture
import "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/keepwarm"
type NestedGroup struct {
	Manager *keepwarm.Manager
}
`,
			},
			target:     "OuterAggregate",
			exceptions: nil,
			wantCount:  1,
		},
		{
			name: "inline anonymous struct hides forbidden field name",
			sources: map[string]string{"synthetic.go": `package fixture
type InlineAggregate struct {
	Nested struct {
		SecretGuardDecisionMetrics int
	}
}
`},
			target:     "InlineAggregate",
			exceptions: nil,
			wantCount:  1,
		},
		{
			name: "textual exception match spoofs foreign package",
			sources: map[string]string{"synthetic.go": `package fixture
import extensions "github.com/example/fake/extensions"
type SpoofAggregate struct {
	SecretGuard *extensions.SecretGuardPlane
}
`},
			target:     "SpoofAggregate",
			exceptions: map[string]string{"SecretGuard": realSGPlane},
			wantCount:  1,
		},
		{
			name: "control: genuine approved reference stays silent",
			sources: map[string]string{"synthetic.go": `package fixture
import "github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
type GenuineAggregate struct {
	SecretGuard *extensions.SecretGuardPlane
}
`},
			target:     "GenuineAggregate",
			exceptions: map[string]string{"SecretGuard": realSGPlane},
			wantCount:  0,
		},
		{
			name: "control: alias to approved reference stays silent",
			sources: map[string]string{"synthetic.go": `package fixture
import "github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
type PlaneAlias = extensions.SecretGuardPlane
type AliasApprovedAggregate struct {
	SecretGuard *PlaneAlias
}
`},
			target:     "AliasApprovedAggregate",
			exceptions: map[string]string{"SecretGuard": realSGPlane},
			wantCount:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope := archParseSources(t, tc.sources)
			got := scope.scan(tc.target, tc.exceptions)
			if tc.wantCount == 0 {
				if len(got) != 0 {
					t.Fatalf("expected zero violations, got %d:\n%s", len(got), strings.Join(got, "\n"))
				}
				return
			}
			if len(got) < tc.wantCount {
				t.Fatalf("expected at least %d violations for %s, got %d", tc.wantCount, tc.target, len(got))
			}
		})
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
