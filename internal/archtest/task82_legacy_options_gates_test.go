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
)

const (
	legacyOptionsAdapterPath = "pkg/lipruntime/legacy_options.go"
	legacyOptionsDeclPath    = "pkg/lipruntime/options.go"
	legacyNormalizePath      = "pkg/lipruntime/normalize.go"
	legacyProdOptionsPath    = "internal/infra/runtimebundle/production_options.go"
	legacyRuntimebundleDir   = "internal/infra/runtimebundle"
	legacyRuntimehostDir     = "internal/infra/runtimehost"
	legacyLipruntimeDir      = "pkg/lipruntime"
)

// Fixed deprecated public Options fields for the current major. Task 8.4 removes
// them; until then this set must not grow (req 10.7) and may appear in production
// only in options.go (declarations) and legacy_options.go (quarantine adapter).
var legacyFixedDeprecatedOptionFields = []string{
	"RequestProviders",
	"AttemptProviders",
	"ConcurrencyProvider",
	"Rater",
	"ProviderDescriptors",
}

var legacyFixedDeprecatedOptionFieldSet = func() map[string]bool {
	out := make(map[string]bool, len(legacyFixedDeprecatedOptionFields))
	for _, name := range legacyFixedDeprecatedOptionFields {
		out[name] = true
	}
	return out
}()

// Adapter helpers quarantined in legacy_options.go; forbidden elsewhere in
// pkg/lipruntime production (req 10.5-10.6, 12.5).
var legacyAdapterHelperIdents = []string{
	"filterDescriptorsByFamily",
	"descriptorHasFamily",
	"legacyRequestRegistrations",
	"legacyAttemptRegistrations",
	"legacyConcurrencyRegistration",
	"legacyProductionRaterID",
}

// Always-forbidden in internal runtimebundle/runtimehost production (no
// ambiguity with canonical concurrency/rater concepts).
var legacyForbiddenInternalExactIdents = []string{
	"RequestProviders",
	"AttemptProviders",
	"ProviderDescriptors",
	"legacyProductionRaterID",
	"rejectUnboundLegacyAuthority",
}

// Ambiguous names: may be legacy option fields or legitimate canonical types/
// AccountingRuntime / RaterRegistration members. Detected via field-shape AST.
var legacyAmbiguousDeprecatedFields = []string{
	"ConcurrencyProvider",
	"Rater",
}

func TestLegacyOptions_Boundary_AdapterFileExists(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(legacyOptionsAdapterPath))
	if !fileExists(path) {
		t.Fatalf("expected quarantine adapter %s (Task 8.2)", legacyOptionsAdapterPath)
	}
}

func TestLegacyOptions_Boundary_DeprecatedFieldsOnlyInPublicAdapter(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var bad []string
	err := walkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		rel = slashPath(rel)
		if !legacyInScopedProduction(rel) {
			return nil
		}
		for _, finding := range scanLegacyOptionsLeak(t, rel, src) {
			bad = append(bad, fmt.Sprintf("%s: %s", rel, finding))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("legacy option leakage below public adapter (%d):\n%s", len(bad), strings.Join(bad, "\n"))
	}
}

func TestLegacyOptions_Canonical_NormalizeHasNoLegacyHelpers(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(legacyNormalizePath))
	src, err := readFileBytes(path)
	if err != nil {
		t.Fatal(err)
	}
	findings := scanLegacyOptionsLeak(t, legacyNormalizePath, src)
	if len(findings) > 0 {
		t.Fatalf("%s must not contain legacy pairing/filtering/deprecated reads: %v", legacyNormalizePath, findings)
	}
}

func TestLegacyOptions_Canonical_ProductionOptionsRegistrationOnly(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(legacyProdOptionsPath))
	src, err := readFileBytes(path)
	if err != nil {
		t.Fatal(err)
	}
	fields := structFieldNames(t, path, src, "ProductionOptions")
	var bad []string
	for _, f := range legacyFixedDeprecatedOptionFields {
		if fields[f] {
			bad = append(bad, f)
		}
	}
	required := []string{
		"RequestRegistrations", "AttemptRegistrations",
		"ConcurrencyRegistration", "RaterRegistrations",
	}
	for _, f := range required {
		if !fields[f] {
			bad = append(bad, "missing "+f)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("ProductionOptions must be canonical registrations only; violations: %v", bad)
	}
}

func TestLegacyOptions_Boundary_InternalPackagesCannotObserveLegacyFields(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var bad []string
	err := walkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		rel = slashPath(rel)
		if !legacyInInternalProduction(rel) {
			return nil
		}
		for _, finding := range scanLegacyOptionsLeak(t, rel, src) {
			bad = append(bad, fmt.Sprintf("%s: %s", rel, finding))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("internal runtimebundle/runtimehost still observes legacy option fields (%d):\n%s", len(bad), strings.Join(bad, "\n"))
	}
}

func TestLegacyOptions_Boundary_NoDeprecatedFieldGrowth(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(legacyOptionsDeclPath))
	src, err := readFileBytes(path)
	if err != nil {
		t.Fatal(err)
	}
	deprecated := deprecatedFieldNamesFromStruct(t, path, src, "Options")
	var unexpected []string
	for name := range deprecated {
		if !legacyFixedDeprecatedOptionFieldSet[name] {
			unexpected = append(unexpected, name)
		}
	}
	var missing []string
	for _, name := range legacyFixedDeprecatedOptionFields {
		if !deprecated[name] {
			missing = append(missing, name)
		}
	}
	if len(unexpected) > 0 || len(missing) > 0 {
		t.Fatalf("deprecated Options field set drift: unexpected=%v missing=%v (fixed list must not grow; Task 8.4 owns removal)", unexpected, missing)
	}
}

func TestLegacyOptions_Boundary_ScannerCatchesAliasesAndIndirection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		rel     string
		src     string
		wantHit bool
	}{
		{
			name: "direct_request_providers_field",
			rel:  "internal/infra/runtimebundle/rogue.go",
			src: `package runtimebundle
type ProductionOptions struct{ RequestProviders []int }
`,
			wantHit: true,
		},
		{
			name: "direct_concurrency_provider_field",
			rel:  "internal/infra/runtimebundle/rogue_conc.go",
			src: `package runtimebundle
type ProductionOptions struct{ ConcurrencyProvider int }
`,
			wantHit: true,
		},
		{
			name: "direct_rater_field",
			rel:  "internal/infra/runtimebundle/rogue_rater.go",
			src: `package runtimebundle
type ProductionOptions struct{ Rater int }
`,
			wantHit: true,
		},
		{
			name: "direct_provider_descriptors_field",
			rel:  "internal/infra/runtimebundle/rogue_descs.go",
			src: `package runtimebundle
type ProductionOptions struct{ ProviderDescriptors []int }
`,
			wantHit: true,
		},
		{
			name: "embedded_legacy_struct",
			rel:  "internal/infra/runtimebundle/embed.go",
			src: `package runtimebundle
type legacyBag struct{ RequestProviders []int }
type ProductionOptions struct{ legacyBag }
`,
			wantHit: true,
		},
		{
			name: "embedded_concurrency_provider_holder",
			rel:  "internal/infra/runtimebundle/embed_conc.go",
			src: `package runtimebundle
type legacyBag struct{ ConcurrencyProvider int }
type holder struct{ legacyBag }
`,
			wantHit: true,
		},
		{
			name: "helper_reads_provider_descriptors",
			rel:  "internal/infra/runtimebundle/helper_descs.go",
			src: `package runtimebundle
func read(p struct{ ProviderDescriptors int }) int { return p.ProviderDescriptors }
`,
			wantHit: true,
		},
		{
			name: "helper_reads_concurrency_provider",
			rel:  "internal/infra/runtimebundle/helper_conc.go",
			src: `package runtimebundle
func read(p struct{ ConcurrencyProvider int }) int { return p.ConcurrencyProvider }
`,
			wantHit: true,
		},
		{
			name: "factory_callback_stores_rater",
			rel:  "internal/infra/runtimebundle/factory_rater.go",
			src: `package runtimebundle
type raterStore struct{ Rater int }
func newStore(r int) *raterStore { return &raterStore{Rater: r} }
func bind(cb func(raterStore)) { cb(raterStore{Rater: 1}) }
`,
			wantHit: true,
		},
		{
			name: "factory_stores_provider_descriptors",
			rel:  "internal/infra/runtimebundle/factory_descs.go",
			src: `package runtimebundle
type descBox struct{ ProviderDescriptors []int }
func store(d []int) descBox { return descBox{ProviderDescriptors: d} }
`,
			wantHit: true,
		},
		{
			name: "legacy_rater_id_literal",
			rel:  "internal/infra/runtimebundle/rater.go",
			src: `package runtimebundle
const id = "legacy-production-rater"
`,
			wantHit: true,
		},
		{
			name: "normalize_filter_helper",
			rel:  "pkg/lipruntime/normalize.go",
			src: `package lipruntime
func filterDescriptorsByFamily() {}
`,
			wantHit: true,
		},
		{
			name: "public_facade_reads_opts_rater",
			rel:  "pkg/lipruntime/facade.go",
			src: `package lipruntime
func leak(opts Options) { _ = opts.Rater }
`,
			wantHit: true,
		},
		{
			name: "public_build_reads_opts_provider_descriptors",
			rel:  "pkg/lipruntime/build.go",
			src: `package lipruntime
func leak(opts Options) { _ = opts.ProviderDescriptors }
`,
			wantHit: true,
		},
		{
			name: "unrelated_concurrency_interface_use",
			rel:  "internal/infra/runtimebundle/ok.go",
			src: `package runtimebundle
import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
func use(p authority.ConcurrencyProvider) authority.ConcurrencyProvider { return p }
`,
			wantHit: false,
		},
		{
			name: "concurrency_registration_provider",
			rel:  "internal/infra/runtimebundle/ok_reg.go",
			src: `package runtimebundle
func use(prod struct{ ConcurrencyRegistration *struct{ Provider int } }) int {
	if prod.ConcurrencyRegistration == nil {
		return 0
	}
	return prod.ConcurrencyRegistration.Provider
}
`,
			wantHit: false,
		},
		{
			name: "accounting_runtime_concurrency_provider",
			rel:  "internal/infra/runtimebundle/ok_rt.go",
			src: `package runtimebundle
func attach(rt *AccountingRuntime) {
	if rt.ConcurrencyProvider == nil {
		return
	}
	_ = rt.ConcurrencyProvider
}
func attachAccounting(accountingRT *AccountingRuntime) {
	_ = accountingRT.ConcurrencyProvider
}
`,
			wantHit: false,
		},
		{
			name: "canonical_registration_field",
			rel:  "internal/infra/runtimebundle/ok2.go",
			src: `package runtimebundle
type ProductionOptions struct {
	ConcurrencyRegistration *int
	RaterRegistrations []int
}
func selectEconomicsRater() {}
func use(ex struct{ EconomicsRater int }) int { return ex.EconomicsRater }
`,
			wantHit: false,
		},
		{
			name: "rater_registration_member_access",
			rel:  "internal/infra/runtimebundle/ok_reg_rater.go",
			src: `package runtimebundle
import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
func pick(regs []economics.RaterRegistration) economics.Rater {
	var operator economics.Rater
	for _, reg := range regs {
		operator = reg.Rater
	}
	return operator
}
`,
			wantHit: false,
		},
		{
			name: "rater_registration_composite_key",
			rel:  "internal/infra/runtimebundle/ok_rr_lit.go",
			src: `package runtimebundle
import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
func makeRegs(r economics.Rater) []economics.RaterRegistration {
	return []economics.RaterRegistration{{
		ID: "x", Rater: r,
	}}
}
`,
			wantHit: false,
		},
		{
			name: "allowed_adapter_file",
			rel:  legacyOptionsAdapterPath,
			src: `package lipruntime
func adapt() { _ = "legacy-production-rater"; var RequestProviders int }
`,
			wantHit: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := scanLegacyOptionsLeak(t, tc.rel, []byte(tc.src))
			hit := len(got) > 0
			if hit != tc.wantHit {
				t.Fatalf("hit=%v want %v findings=%v", hit, tc.wantHit, got)
			}
		})
	}
}

func legacyInScopedProduction(rel string) bool {
	return strings.HasPrefix(rel, legacyLipruntimeDir+"/") ||
		legacyInInternalProduction(rel)
}

func legacyInInternalProduction(rel string) bool {
	return strings.HasPrefix(rel, legacyRuntimebundleDir+"/") ||
		strings.HasPrefix(rel, legacyRuntimehostDir+"/")
}

func legacyAllowedFiles() map[string]bool {
	return map[string]bool{
		legacyOptionsDeclPath:    true,
		legacyOptionsAdapterPath: true,
	}
}

// scanLegacyOptionsLeak reports deprecated public-option leakage for one
// production-shaped source unit. Shared by permanent gates and scanner fixtures.
func scanLegacyOptionsLeak(t *testing.T, rel string, src []byte) []string {
	t.Helper()
	rel = slashPath(rel)
	if legacyAllowedFiles()[rel] {
		return nil
	}
	inInternal := legacyInInternalProduction(rel)
	inLipruntime := strings.HasPrefix(rel, legacyLipruntimeDir+"/")
	if !inInternal && !inLipruntime {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	var bad []string
	if inLipruntime {
		bad = append(bad, legacyScanLipruntimeProduction(f)...)
	}
	if inInternal {
		bad = append(bad, legacyScanInternalProduction(f)...)
	}
	return uniqueStrings(bad)
}

func legacyScanLipruntimeProduction(f *ast.File) []string {
	var bad []string
	forbidden := append(append([]string{}, legacyFixedDeprecatedOptionFields...), legacyAdapterHelperIdents...)
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			for _, id := range forbidden {
				if x.Name == id {
					bad = append(bad, id)
				}
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING && strings.Contains(x.Value, "legacy-production-rater") {
				bad = append(bad, "legacy-production-rater")
			}
		}
		return true
	})
	return bad
}

func legacyScanInternalProduction(f *ast.File) []string {
	var bad []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			for _, id := range legacyForbiddenInternalExactIdents {
				if x.Name == id {
					bad = append(bad, id)
				}
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING && strings.Contains(x.Value, "legacy-production-rater") {
				bad = append(bad, "legacy-production-rater")
			}
		}
		return true
	})
	for _, field := range legacyAmbiguousDeprecatedFields {
		bad = append(bad, legacyScanAmbiguousFieldLeak(f, field)...)
	}
	return bad
}

// legacyScanAmbiguousFieldLeak finds ConcurrencyProvider/Rater used as legacy
// option field shapes while allowing authority.ConcurrencyProvider /
// economics.Rater types, AccountingRuntime.ConcurrencyProvider, and
// RaterRegistration.Rater.
func legacyScanAmbiguousFieldLeak(f *ast.File, field string) []string {
	var bad []string
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return true
		}
		stack = append(stack, n)
		switch x := n.(type) {
		case *ast.Field:
			for _, name := range x.Names {
				if name != nil && name.Name == field {
					bad = append(bad, "field "+field)
				}
			}
		case *ast.KeyValueExpr:
			if id, ok := x.Key.(*ast.Ident); ok && id.Name == field {
				if field == "Rater" && legacyCompositeKeyIsRaterRegistration(stack) {
					break
				}
				bad = append(bad, "composite "+field)
			}
		case *ast.SelectorExpr:
			if x.Sel == nil || x.Sel.Name != field {
				break
			}
			if legacyExprInTypePosition(stack) {
				break
			}
			if field == "ConcurrencyProvider" && legacyAllowedConcurrencyProviderRecv(x.X) {
				break
			}
			if field == "Rater" && legacyAllowedRaterRecv(x.X) {
				break
			}
			bad = append(bad, "selector "+field)
		}
		return true
	})
	return bad
}

func legacyAllowedConcurrencyProviderRecv(x ast.Expr) bool {
	id, ok := x.(*ast.Ident)
	if !ok {
		return false
	}
	switch id.Name {
	case "rt", "accountingRT":
		return true
	default:
		return false
	}
}

func legacyAllowedRaterRecv(x ast.Expr) bool {
	id, ok := x.(*ast.Ident)
	return ok && id.Name == "reg"
}

func legacyExprInTypePosition(stack []ast.Node) bool {
	// stack ends with the SelectorExpr; inspect parents for type contexts.
	for i := len(stack) - 2; i >= 0; i-- {
		switch p := stack[i].(type) {
		case *ast.Field:
			return p.Type == stack[i+1] || continuesTypeWrapper(stack[i+1], p.Type)
		case *ast.ValueSpec:
			return p.Type == stack[i+1] || continuesTypeWrapper(stack[i+1], p.Type)
		case *ast.TypeSpec:
			return p.Type == stack[i+1] || continuesTypeWrapper(stack[i+1], p.Type)
		case *ast.ArrayType:
			return p.Elt == stack[i+1] || continuesTypeWrapper(stack[i+1], p.Elt)
		case *ast.MapType:
			return p.Key == stack[i+1] || p.Value == stack[i+1] ||
				continuesTypeWrapper(stack[i+1], p.Key) || continuesTypeWrapper(stack[i+1], p.Value)
		case *ast.ChanType:
			return p.Value == stack[i+1] || continuesTypeWrapper(stack[i+1], p.Value)
		case *ast.StarExpr, *ast.ParenExpr:
			// Keep walking — may wrap a type (*T) or a value (*ptr).
			continue
		case *ast.FuncType:
			return false
		case *ast.InterfaceType, *ast.StructType:
			return false
		case *ast.CallExpr:
			// Conversion T(x) uses CallExpr with Fun as the type.
			if p.Fun == stack[i+1] || continuesTypeWrapper(stack[i+1], p.Fun) {
				return true
			}
			return false
		case *ast.AssignStmt, *ast.ReturnStmt, *ast.BinaryExpr,
			*ast.UnaryExpr, *ast.IndexExpr, *ast.SliceExpr,
			*ast.KeyValueExpr, *ast.CompositeLit, *ast.SendStmt, *ast.RangeStmt,
			*ast.IfStmt, *ast.ForStmt, *ast.SwitchStmt, *ast.TypeAssertExpr:
			return false
		}
	}
	return false
}

// continuesTypeWrapper reports whether child is nested under typeExpr via
// pointer/paren wrappers (*T / (T)).
func continuesTypeWrapper(child ast.Node, typeExpr ast.Expr) bool {
	cur := typeExpr
	for cur != nil {
		if cur == child {
			return true
		}
		switch x := cur.(type) {
		case *ast.StarExpr:
			cur = x.X
		case *ast.ParenExpr:
			cur = x.X
		default:
			return false
		}
	}
	return false
}

func legacyCompositeKeyIsRaterRegistration(stack []ast.Node) bool {
	for i := len(stack) - 1; i >= 0; i-- {
		cl, ok := stack[i].(*ast.CompositeLit)
		if !ok {
			continue
		}
		if legacyTypeNamesRaterRegistration(cl.Type) {
			return true
		}
		// []RaterRegistration{{ Rater: x }} — inner lit has nil Type.
		if cl.Type == nil && i > 0 {
			if parent, ok := stack[i-1].(*ast.CompositeLit); ok {
				if at, ok := parent.Type.(*ast.ArrayType); ok && legacyTypeNamesRaterRegistration(at.Elt) {
					return true
				}
			}
		}
		return false
	}
	return false
}

func legacyTypeNamesRaterRegistration(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name == "RaterRegistration"
	case *ast.SelectorExpr:
		return x.Sel != nil && x.Sel.Name == "RaterRegistration"
	case *ast.StarExpr:
		return legacyTypeNamesRaterRegistration(x.X)
	case *ast.ArrayType:
		return legacyTypeNamesRaterRegistration(x.Elt)
	default:
		return false
	}
}

func structFieldNames(t *testing.T, filename string, src []byte, typeName string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	out := map[string]bool{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name == nil || ts.Name.Name != typeName {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			for _, field := range st.Fields.List {
				if len(field.Names) == 0 {
					out[exprBaseName(field.Type)] = true
					continue
				}
				for _, n := range field.Names {
					if n != nil {
						out[n.Name] = true
					}
				}
			}
		}
	}
	return out
}

func deprecatedFieldNamesFromStruct(t *testing.T, filename string, src []byte, typeName string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	out := map[string]bool{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name == nil || ts.Name.Name != typeName {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			for _, field := range st.Fields.List {
				doc := ""
				if field.Doc != nil {
					doc = field.Doc.Text()
				}
				if field.Comment != nil {
					doc += field.Comment.Text()
				}
				if !strings.Contains(strings.ToLower(doc), "deprecated") {
					continue
				}
				for _, n := range field.Names {
					if n != nil {
						out[n.Name] = true
					}
				}
			}
		}
	}
	return out
}

func exprBaseName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return exprBaseName(x.X)
	case *ast.SelectorExpr:
		return x.Sel.Name
	default:
		return "embedded"
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readFileBytes(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
