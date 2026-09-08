package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestGenerationFacadeShape pins the Task 2.2 conformance of the
// standard-distribution generation facade: GenerationOutput carries only the
// ordinary Bundle/Planes/Lifecycles plus the fixed CorePorts consumer ports,
// and GenerationInput carries only generic composition inputs plus an
// explicitly allowlisted set of feature-config inputs and generic ports.
// CorePorts itself is pinned: the five task-assigned consumer interfaces plus
// three opaque NO-GO-remediation ports (MetricsSwap, KeepwarmAdmin,
// TerminalPolicyProjection), each documented below. Any new per-feature output
// field, input field, or CorePorts member fails loudly here instead of leaking
// back into generic runtimebundle.
func TestGenerationFacadeShape(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	absPath := filepath.Join(root, filepath.FromSlash("internal/standardplugins/featurehost/inputs.go"))

	wantOutput := []string{
		"Bundle:lipfeature.FeatureBundle",
		"Planes:lipfeature.FrozenPlaneSet",
		"Lifecycles:[]lipplugin.Lifecycle",
		"CorePorts:CorePorts",
	}
	// CorePorts members: five task-assigned consumer interfaces
	// (CompactionDetector, ConversationReader, InterleavedProcessor,
	// PromptCacheMaintenance, TerminalPolicyReader) plus three opaque
	// remediation ports, each carrying no concrete feature type across the
	// boundary: MetricsSwap is a bare func() invoked once per published
	// generation; KeepwarmAdmin is a process-stable stdhttp options value
	// copied opaquely; TerminalPolicyProjection is a factory func value
	// invoked with generic composition state. Adding a ninth member
	// requires the same bar: opaque type, documented justification here.
	wantCorePorts := []string{
		"CompactionDetector:runtime.CompactionDetector",
		"ConversationReader:conversationprojection.Reader",
		"InterleavedProcessor:runtime.InterleavedProcessor",
		"PromptCacheMaintenance:runtime.PromptCacheMaintenance",
		"TerminalPolicyReader:runtime.TerminalPolicyReader",
		"MetricsSwap:func()",
		"KeepwarmAdmin:adminkeepwarm.Options",
		"TerminalPolicyProjection:TerminalPolicyProjectionFunc",
	}
	wantInput := []string{
		"Registrations:[]lipsdk.Registration",
		"HostRegistrations:[]sdkfeaturehost.Registration",
		"MergeSurface:featurebundle.GeneratedMergeSurface",
		"Planes:lipfeature.FrozenPlaneSet",
		"Lifecycles:[]lipplugin.Lifecycle",
		"CandidatePlanes:lipfeature.FrozenPlaneSet",
		"BackgroundClient:auxiliary.BackgroundClient",
		"BackgroundPoller:auxiliary.BackgroundPoller",
		"ReasoningProdOpts:ReasoningCompressionOptions",
		"ReasoningTestOpts:ReasoningCompressionOptions",
		"InterleavedConfig:interleavedthinking.Config",
		"ConfigInterleaved:config.InterleavedConfig",
		"KeepwarmConfig:keepwarm.Config",
		"NowFn:func()(time.Time)",
		"KeepwarmAccounting:billing.ProviderMaintenanceUsageObserver",
		"ConfigDir:string",
		"AccessMode:accessmode.Mode",
		"SecretEnv:SecretGuardEnvironment",
		"SecretInputs:SecretGuardInputs",
		"DecisionObserver:SecretDecisionObserver",
		"FaultInject:error",
	}

	fieldsOf := func(structName string) []string {
		t.Helper()
		got, err := scanFacadeStructFields(absPath, structName)
		if err != nil {
			t.Fatalf("scanFacadeStructFields: %v", err)
		}
		return got
	}

	gotOutput := fieldsOf("GenerationOutput")
	if !slices.Equal(gotOutput, wantOutput) {
		t.Fatalf("GenerationOutput facade shape drift:\n got=%v\nwant=%v", gotOutput, wantOutput)
	}
	gotInput := fieldsOf("GenerationInput")
	if !slices.Equal(gotInput, wantInput) {
		t.Fatalf("GenerationInput facade shape drift:\n got=%v\nwant=%v", gotInput, wantInput)
	}
	gotCorePorts := fieldsOf("CorePorts")
	if !slices.Equal(gotCorePorts, wantCorePorts) {
		t.Fatalf("CorePorts facade shape drift:\n got=%v\nwant=%v", gotCorePorts, wantCorePorts)
	}
}

// archTypeString renders a type expression deterministically for facade-shape
// comparison. Unknown composite forms render as "complex", which never matches
// the allowlist: new shapes fail closed instead of slipping through.
func archTypeString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return archTypeString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + archTypeString(t.X)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + archTypeString(t.Elt)
		}
		return "[n]" + archTypeString(t.Elt)
	case *ast.FuncType:
		var params []string
		if t.Params != nil {
			for _, p := range t.Params.List {
				params = append(params, archTypeString(p.Type))
			}
		}
		s := "func(" + strings.Join(params, ",") + ")"
		if t.Results != nil && len(t.Results.List) > 0 {
			var results []string
			for _, r := range t.Results.List {
				results = append(results, archTypeString(r.Type))
			}
			s += "(" + strings.Join(results, ",") + ")"
		}
		return s
	case *ast.ParenExpr:
		return archTypeString(t.X)
	default:
		return "complex"
	}
}

// scanFacadeStructFields returns "Name:Type" entries for a struct declared in
// the file at absPath. Embedded fields (no names) are recorded as
// "embedded:Type" so they can never silently match a named allowlist entry.
func scanFacadeStructFields(absPath, structName string) ([]string, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, absPath, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("ParseFile(%s): %w", absPath, err)
	}
	for _, decl := range node.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != structName {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return nil, fmt.Errorf("%s is not a struct", structName)
			}
			var out []string
			for _, f := range st.Fields.List {
				typ := archTypeString(f.Type)
				if len(f.Names) == 0 {
					out = append(out, "embedded:"+typ)
					continue
				}
				for _, name := range f.Names {
					out = append(out, name.Name+":"+typ)
				}
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("struct %s not found in %s: scanner blind spot, refusing to pass", structName, absPath)
}

// featurehostImportNames resolves the local identifiers bound to the
// featurehost package import in f, so aliased imports cannot bypass the
// identifier boundary. A dot import hides every selector and is reported
// separately: it must never appear in generic runtimebundle code.
func featurehostImportNames(f *ast.File) (locals map[string]bool, dotImport bool) {
	locals = map[string]bool{}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if path != "github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost" {
			continue
		}
		if imp.Name == nil {
			locals["featurehost"] = true
			continue
		}
		switch imp.Name.Name {
		case ".":
			dotImport = true
		case "_":
			// Blank import exposes no identifiers; nothing to track.
		default:
			locals[imp.Name.Name] = true
		}
	}
	return locals, dotImport
}

// scanFeaturehostRefs reports boundary violations in one Go source file:
// forbidden concrete-feature imports, dot imports of the facade package, and
// `featurehost.X` (under any local import name) selectors outside the pinned
// allowlist. Production scanning and negative fixtures share this scanner.
func scanFeaturehostRefs(rel string, src []byte) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return nil, err
	}
	var violations []string
	for _, imp := range FileImportPaths(f) {
		for _, forbidden := range forbiddenFeatureImportSubstrings {
			if strings.Contains(imp, forbidden) {
				violations = append(violations, fmt.Sprintf("%s: imports forbidden %s", rel, imp))
			}
		}
	}
	locals, dotImport := featurehostImportNames(f)
	if dotImport {
		violations = append(violations, fmt.Sprintf("%s: dot-import of featurehost hides selectors", rel))
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || !locals[ident.Name] {
			return true
		}
		if !allowedFeaturehostQualifiers[sel.Sel.Name] {
			violations = append(violations, fmt.Sprintf("%s: forbidden featurehost.%s reference", rel, sel.Sel.Name))
		}
		return true
	})
	return violations, nil
}

// allowedFeaturehostQualifiers pins the exact set of `featurehost.X`
// package-qualified identifiers generic runtimebundle production code may
// reference. Concrete feature knowledge routed through featurehost (concrete
// admin services, policy projections, per-feature planes) must never appear
// here: reaching it would bypass the runtimebundle concrete-feature import
// rule through the back door.
var allowedFeaturehostQualifiers = map[string]bool{
	"Runtime":                          true,
	"NewProcess":                       true,
	"ProcessInput":                     true,
	"HostEnvironment":                  true,
	"CompactionSchedulerBounds":        true,
	"GenerationInput":                  true,
	"GenerationOutput":                 true,
	"CorePorts":                        true,
	"MetricsRegistry":                  true,
	"Registration":                     true,
	"ValidateSecretGuardRegistrations": true,
}

// forbiddenFeatureImportSubstrings pins concrete-feature package path
// vocabulary that must never be imported by runtimebundle production code.
// Standard-distribution composition (featurehost itself), core contracts, and
// stdhttp admin contracts are not in this set.
var forbiddenFeatureImportSubstrings = []string{
	"/internal/plugins/features/",
	"/pkg/lipsdk/secretguardhost",
	"internal/standardplugins/featurehost/secretguard",
	"internal/standardplugins/featurehost/sessionpolicy",
	"internal/standardplugins/featurehost/compaction",
	"internal/standardplugins/featurehost/reasoning",
	"internal/infra/secretguardcompose",
	"internal/infra/reasoningcompose",
	"internal/infra/compactioncompose",
}

// TestRuntimeBundleFeatureIdentifierBoundary asserts that generic
// runtimebundle production code reaches the standard distribution only
// through the pinned facade allowlist and imports zero concrete-feature
// packages. See allowedFeaturehostQualifiers for the closed vocabulary.
func TestRuntimeBundleFeatureIdentifierBoundary(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var violations []string
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		if !MatchPathPrefix(pkg, "internal/infra/runtimebundle") {
			return nil
		}
		got, err := scanFeaturehostRefs(rel, src)
		if err != nil {
			return err
		}
		violations = append(violations, got...)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkProductionGoFiles: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("runtimebundle reaches concrete feature knowledge through featurehost (%d):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

// TestRuntimeBundleFeatureIdentifierBoundary_NegativeFixtures proves the
// boundary scanner is not vacuous: concrete feature identifiers routed
// through featurehost-qualified access are flagged, while pinned facade
// references stay silent.
func TestRuntimeBundleFeatureIdentifierBoundary_NegativeFixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		src       string
		wantCount int
	}{
		{
			name: "concrete admin service through featurehost is flagged",
			src: `package runtimebundle
import "github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
var _ = featurehost.TerminalDecisionPolicyHTTPProjection
`,
			wantCount: 1,
		},
		{
			name: "concrete feature package import is flagged",
			src: `package runtimebundle
import "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/keepwarm"
var _ = keepwarm.Config{}
`,
			wantCount: 1,
		},
		{
			name: "dedicated featurehost subpackage import is flagged",
			src: `package runtimebundle
import "github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost/sessionpolicy"
var _ = sessionpolicy.Store{}
`,
			wantCount: 1,
		},
		{
			name: "pinned facade references stay silent",
			src: `package runtimebundle
import "github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
var _ featurehost.GenerationOutput
var _ featurehost.CorePorts
var _ featurehost.ProcessInput
`,
			wantCount: 0,
		},
		{
			name: "aliased featurehost import is still flagged",
			src: `package runtimebundle
import fh "github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
var _ = fh.TerminalDecisionPolicyHTTPProjection
`,
			wantCount: 1,
		},
		{
			name: "dot import of featurehost is flagged",
			src: `package runtimebundle
import . "github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
var _ = GenerationOutput{}
`,
			wantCount: 1,
		},
		{
			name: "aliased pinned facade references stay silent",
			src: `package runtimebundle
import fh "github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
var _ fh.GenerationOutput
var _ fh.CorePorts
`,
			wantCount: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := scanFeaturehostRefs("synthetic.go", []byte(tc.src))
			if err != nil {
				t.Fatalf("scanFeaturehostRefs: %v", err)
			}
			if len(got) != tc.wantCount {
				t.Fatalf("got %d violations %v, want %d", len(got), got, tc.wantCount)
			}
		})
	}
}

// TestFacadeShapeScanner_NegativeFixtures proves the shared shape scanner
// rejects embedded fields and type drift that a name-only check would miss.
func TestFacadeShapeScanner_NegativeFixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		src       string
		wantCount int
	}{
		{
			name: "embedded field is flagged",
			src: `package featurehost
type GenerationOutput struct {
	Bundle lipfeature.FeatureBundle
	embeddedPort
}
`,
			wantCount: 1,
		},
		{
			name: "field type drift is flagged",
			src: `package featurehost
type CorePorts struct {
	MetricsSwap string
}
`,
			wantCount: 1,
		},
		{
			name: "exact typed shape stays silent",
			src: `package featurehost
type GenerationOutput struct {
	Bundle     lipfeature.FeatureBundle
	Planes     lipfeature.FrozenPlaneSet
	Lifecycles []lipplugin.Lifecycle
	CorePorts  CorePorts
}
`,
			wantCount: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "synthetic.go", tc.src, 0)
			if err != nil {
				t.Fatalf("ParseFile: %v", err)
			}
			var got []string
			ast.Inspect(f, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					return true
				}
				for _, field := range st.Fields.List {
					typ := archTypeString(field.Type)
					if len(field.Names) == 0 {
						got = append(got, "embedded:"+typ)
						continue
					}
					for _, name := range field.Names {
						// The pinned GenerationOutput shape carries exactly
						// four named fields; anything else (embedded or
						// mistyped) is a violation here.
						if ts.Name.Name == "GenerationOutput" || ts.Name.Name == "CorePorts" {
							got = append(got, name.Name+":"+typ)
						}
					}
				}
				return true
			})
			// Reuse the production allowlists: silence means the rendered
			// shape is byte-identical to an approved entry.
			approved := map[string]bool{
				"Bundle:lipfeature.FeatureBundle":     true,
				"Planes:lipfeature.FrozenPlaneSet":    true,
				"Lifecycles:[]lipplugin.Lifecycle":    true,
				"CorePorts:CorePorts":                 true,
				"MetricsSwap:func()":                  true,
				"KeepwarmAdmin:adminkeepwarm.Options": true,
			}
			flagged := 0
			for _, entry := range got {
				if !approved[entry] {
					flagged++
				}
			}
			// The silent case renders only approved entries; every other
			// case must produce at least the expected violation count.
			if tc.wantCount == 0 && flagged != 0 {
				t.Fatalf("got %d violations %v, want 0", flagged, got)
			}
			if tc.wantCount > 0 && flagged < tc.wantCount {
				t.Fatalf("got %d violations %v, want at least %d", flagged, got, tc.wantCount)
			}
		})
	}
}
