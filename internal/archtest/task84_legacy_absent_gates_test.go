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
	legacyAbsentAdapterPath   = "pkg/lipruntime/legacy_options.go"
	legacyAbsentOptionsPath   = "pkg/lipruntime/options.go"
	legacyAbsentNormalizePath = "pkg/lipruntime/normalize.go"
	legacyAbsentProdOptsPath  = "internal/infra/runtimebundle/production_options.go"
	legacyAbsentBundleDir     = "internal/infra/runtimebundle"
	legacyAbsentHostDir       = "internal/infra/runtimehost"
	legacyAbsentLipruntimeDir = "pkg/lipruntime"
)

// Deleted public Options fields (Task 8.4). Must not reappear in production.
var legacyAbsentDeletedOptionFields = []string{
	"RequestProviders",
	"AttemptProviders",
	"ConcurrencyProvider",
	"Rater",
	"ProviderDescriptors",
}

// Deleted adapter helpers / ids (Task 8.4).
var legacyAbsentDeletedHelperIdents = []string{
	"adaptLegacyOptions",
	"prepareCanonicalProduction",
	"filterDescriptorsByFamily",
	"descriptorHasFamily",
	"legacyRequestRegistrations",
	"legacyAttemptRegistrations",
	"legacyConcurrencyRegistration",
	"legacyProductionRaterID",
	"stageFamily",
	"stageFamilyRequest",
	"stageFamilyAttempt",
	"stageFamilyLease",
}

// Always-forbidden exact idents in internal runtimebundle/runtimehost production.
var legacyAbsentForbiddenInternalExactIdents = []string{
	"RequestProviders",
	"AttemptProviders",
	"ProviderDescriptors",
	"legacyProductionRaterID",
	"adaptLegacyOptions",
	"prepareCanonicalProduction",
	"filterDescriptorsByFamily",
	"descriptorHasFamily",
	"legacyRequestRegistrations",
	"legacyAttemptRegistrations",
	"legacyConcurrencyRegistration",
	"rejectUnboundLegacyAuthority",
	"stageFamily",
	"stageFamilyRequest",
	"stageFamilyAttempt",
	"stageFamilyLease",
}

// Ambiguous names: may be legacy option fields or legitimate canonical types/
// AccountingRuntime / RaterRegistration members. Detected via field-shape AST.
var legacyAbsentAmbiguousFields = []string{
	"ConcurrencyProvider",
	"Rater",
}

func TestLegacyAbsent_AdapterFileDeleted(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(legacyAbsentAdapterPath))
	if fileExists(path) {
		t.Fatalf("expected %s deleted (Task 8.4)", legacyAbsentAdapterPath)
	}
}

func TestLegacyAbsent_OptionsHasNoDeletedFields(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(legacyAbsentOptionsPath))
	src, err := readFileBytes(path)
	if err != nil {
		t.Fatal(err)
	}
	fields := structFieldNames(t, path, src, "Options")
	var bad []string
	for _, f := range legacyAbsentDeletedOptionFields {
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
		t.Fatalf("Options must be canonical registrations only; violations: %v", bad)
	}
}

func TestLegacyAbsent_ProductionSymbolsGone(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var bad []string
	err := walkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		rel = slashPath(rel)
		if !legacyAbsentInScopedProduction(rel) {
			return nil
		}
		for _, finding := range scanLegacyAbsentLeak(t, rel, src) {
			bad = append(bad, fmt.Sprintf("%s: %s", rel, finding))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("deleted legacy option symbols still present (%d):\n%s", len(bad), strings.Join(bad, "\n"))
	}
}

func TestLegacyAbsent_NormalizeHasNoLegacyHelpers(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(legacyAbsentNormalizePath))
	src, err := readFileBytes(path)
	if err != nil {
		t.Fatal(err)
	}
	findings := scanLegacyAbsentLeak(t, legacyAbsentNormalizePath, src)
	if len(findings) > 0 {
		t.Fatalf("%s must not contain deleted legacy symbols: %v", legacyAbsentNormalizePath, findings)
	}
}

func TestLegacyAbsent_ProductionOptionsRegistrationOnly(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(legacyAbsentProdOptsPath))
	src, err := readFileBytes(path)
	if err != nil {
		t.Fatal(err)
	}
	fields := structFieldNames(t, path, src, "ProductionOptions")
	var bad []string
	for _, f := range legacyAbsentDeletedOptionFields {
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

func TestLegacyAbsent_InternalPackagesCannotObserveLegacyFields(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var bad []string
	err := walkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		rel = slashPath(rel)
		if !legacyAbsentInInternalProduction(rel) {
			return nil
		}
		for _, finding := range scanLegacyAbsentLeak(t, rel, src) {
			bad = append(bad, fmt.Sprintf("%s: %s", rel, finding))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("internal runtimebundle/runtimehost still observes deleted legacy symbols (%d):\n%s", len(bad), strings.Join(bad, "\n"))
	}
}

func TestLegacyAbsent_ScannerCatchesAliasesAndIndirection(t *testing.T) {
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
			name: "helper_reads_provider_descriptors",
			rel:  "internal/infra/runtimebundle/helper_descs.go",
			src: `package runtimebundle
func read(p struct{ ProviderDescriptors int }) int { return p.ProviderDescriptors }
`,
			wantHit: true,
		},
		{
			name: "fake_rt_name_does_not_evade_concurrency_provider_field",
			rel:  "internal/infra/runtimebundle/fake_rt.go",
			src: `package runtimebundle
func leak(rt struct{ ConcurrencyProvider int }) int { return rt.ConcurrencyProvider }
`,
			wantHit: true,
		},
		{
			name: "fake_accounting_name_does_not_evade_concurrency_provider_field",
			rel:  "internal/infra/runtimebundle/fake_accounting.go",
			src: `package runtimebundle
func leak(accountingRT struct{ ConcurrencyProvider int }) int { return accountingRT.ConcurrencyProvider }
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
			name: "fake_reg_name_does_not_evade_rater_field",
			rel:  "internal/infra/runtimebundle/fake_reg.go",
			src: `package runtimebundle
func leak(reg struct{ Rater int }) int { return reg.Rater }
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
			name: "adapt_legacy_helper",
			rel:  "pkg/lipruntime/build.go",
			src: `package lipruntime
func adaptLegacyOptions() {}
`,
			wantHit: true,
		},
		{
			name: "prepare_canonical_wrapper",
			rel:  "pkg/lipruntime/build.go",
			src: `package lipruntime
func prepareCanonicalProduction() {}
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
			name: "no_adapter_exemption",
			rel:  legacyAbsentAdapterPath,
			src: `package lipruntime
func adaptLegacyOptions() { _ = "legacy-production-rater"; var RequestProviders int }
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
func attach(rt *AccountingRuntime, other interface{}) {
	if rt.ConcurrencyProvider == nil {
		return
	}
	_ = rt.ConcurrencyProvider
	var accountingRT AccountingRuntime
	_ = accountingRT.ConcurrencyProvider
	typed := AccountingRuntime{}
	_ = typed.ConcurrencyProvider
	_ = other
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
	var direct economics.RaterRegistration
	operator = direct.Rater
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := scanLegacyAbsentLeak(t, tc.rel, []byte(tc.src))
			hit := len(got) > 0
			if hit != tc.wantHit {
				t.Fatalf("hit=%v want %v findings=%v", hit, tc.wantHit, got)
			}
		})
	}
}

func legacyAbsentInScopedProduction(rel string) bool {
	return strings.HasPrefix(rel, legacyAbsentLipruntimeDir+"/") ||
		legacyAbsentInInternalProduction(rel)
}

func legacyAbsentInInternalProduction(rel string) bool {
	return strings.HasPrefix(rel, legacyAbsentBundleDir+"/") ||
		strings.HasPrefix(rel, legacyAbsentHostDir+"/")
}

// scanLegacyAbsentLeak reports deleted legacy-option leakage for one
// production-shaped source unit. No whole-file exemptions (Task 8.4).
func scanLegacyAbsentLeak(t *testing.T, rel string, src []byte) []string {
	t.Helper()
	rel = slashPath(rel)
	inInternal := legacyAbsentInInternalProduction(rel)
	inLipruntime := strings.HasPrefix(rel, legacyAbsentLipruntimeDir+"/")
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
		bad = append(bad, legacyAbsentScanLipruntimeProduction(f)...)
	}
	if inInternal {
		bad = append(bad, legacyAbsentScanInternalProduction(f)...)
	}
	return uniqueStrings(bad)
}

func legacyAbsentScanLipruntimeProduction(f *ast.File) []string {
	var bad []string
	forbidden := append(append([]string{}, legacyAbsentDeletedOptionFields...), legacyAbsentDeletedHelperIdents...)
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

func legacyAbsentScanInternalProduction(f *ast.File) []string {
	var bad []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			for _, id := range legacyAbsentForbiddenInternalExactIdents {
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
	for _, field := range legacyAbsentAmbiguousFields {
		bad = append(bad, legacyAbsentScanAmbiguousFieldLeak(f, field)...)
	}
	return bad
}

// legacyAbsentScanAmbiguousFieldLeak finds ConcurrencyProvider/Rater used as
// legacy option field shapes while allowing authority.ConcurrencyProvider /
// economics.Rater types, AccountingRuntime.ConcurrencyProvider, and
// RaterRegistration.Rater.
func legacyAbsentScanAmbiguousFieldLeak(f *ast.File, field string) []string {
	var bad []string
	var stack []ast.Node
	accountingRuntimeIdents := legacyAccountingRuntimeIdents(f)
	raterRegistrationIdents := legacyRaterRegistrationIdents(f)
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
			if field == "ConcurrencyProvider" && legacyAllowedConcurrencyProviderRecv(x.X, accountingRuntimeIdents) {
				break
			}
			if field == "Rater" && legacyAllowedRaterRecv(x.X, raterRegistrationIdents) {
				break
			}
			bad = append(bad, "selector "+field)
		}
		return true
	})
	return bad
}

func legacyAllowedConcurrencyProviderRecv(x ast.Expr, accountingRuntimeIdents map[string]bool) bool {
	id, ok := x.(*ast.Ident)
	return ok && accountingRuntimeIdents[id.Name]
}

func legacyAccountingRuntimeIdents(f *ast.File) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			if x.Type == nil {
				return true
			}
			collectAccountingRuntimeFields(out, x.Type.Params)
			collectAccountingRuntimeFields(out, x.Type.Results)
		case *ast.ValueSpec:
			if legacyTypeNamesAccountingRuntime(x.Type) {
				for _, name := range x.Names {
					if name != nil {
						out[name.Name] = true
					}
				}
				break
			}
			for i, name := range x.Names {
				if name == nil || i >= len(x.Values) {
					continue
				}
				if legacyExprConstructsAccountingRuntime(x.Values[i]) {
					out[name.Name] = true
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				name, ok := lhs.(*ast.Ident)
				if !ok || name.Name == "_" {
					continue
				}
				var rhs ast.Expr
				switch {
				case len(x.Rhs) == len(x.Lhs):
					rhs = x.Rhs[i]
				case len(x.Rhs) == 1:
					rhs = x.Rhs[0]
				default:
					continue
				}
				if legacyExprConstructsAccountingRuntime(rhs) {
					out[name.Name] = true
				}
			}
		}
		return true
	})
	return out
}

func collectAccountingRuntimeFields(out map[string]bool, fields *ast.FieldList) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		if !legacyTypeNamesAccountingRuntime(field.Type) {
			continue
		}
		for _, name := range field.Names {
			if name != nil {
				out[name.Name] = true
			}
		}
	}
}

func legacyExprConstructsAccountingRuntime(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.CompositeLit:
		return legacyTypeNamesAccountingRuntime(x.Type)
	case *ast.UnaryExpr:
		return x.Op == token.AND && legacyExprConstructsAccountingRuntime(x.X)
	case *ast.CallExpr:
		return legacyTypeNamesAccountingRuntime(x.Fun)
	default:
		return false
	}
}

func legacyTypeNamesAccountingRuntime(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name == "AccountingRuntime"
	case *ast.SelectorExpr:
		return x.Sel != nil && x.Sel.Name == "AccountingRuntime"
	case *ast.StarExpr:
		return legacyTypeNamesAccountingRuntime(x.X)
	case *ast.ParenExpr:
		return legacyTypeNamesAccountingRuntime(x.X)
	default:
		return false
	}
}

func legacyAllowedRaterRecv(x ast.Expr, raterRegistrationIdents map[string]bool) bool {
	id, ok := x.(*ast.Ident)
	return ok && raterRegistrationIdents[id.Name]
}

func legacyRaterRegistrationIdents(f *ast.File) map[string]bool {
	regs := map[string]bool{}
	slices := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			if x.Type == nil {
				return true
			}
			collectRaterRegistrationFields(regs, slices, x.Type.Params)
			collectRaterRegistrationFields(regs, slices, x.Type.Results)
		case *ast.ValueSpec:
			registerRaterRegistrationNames(regs, slices, x.Names, x.Type, x.Values)
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				name, ok := lhs.(*ast.Ident)
				if !ok || name.Name == "_" {
					continue
				}
				var rhs ast.Expr
				switch {
				case len(x.Rhs) == len(x.Lhs):
					rhs = x.Rhs[i]
				case len(x.Rhs) == 1:
					rhs = x.Rhs[0]
				default:
					continue
				}
				if legacyExprConstructsRaterRegistration(rhs) {
					regs[name.Name] = true
				}
				if legacyExprConstructsRaterRegistrationSlice(rhs) {
					slices[name.Name] = true
				}
			}
		case *ast.RangeStmt:
			if !legacyRangeOverRaterRegistrations(x.X, slices) {
				return true
			}
			if id, ok := x.Value.(*ast.Ident); ok && id.Name != "_" {
				regs[id.Name] = true
			}
		}
		return true
	})
	return regs
}

func collectRaterRegistrationFields(regs, slices map[string]bool, fields *ast.FieldList) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		isReg := legacyTypeNamesRaterRegistration(field.Type)
		isSlice := legacyTypeNamesRaterRegistrationSlice(field.Type)
		if !isReg && !isSlice {
			continue
		}
		for _, name := range field.Names {
			if name == nil {
				continue
			}
			if isReg {
				regs[name.Name] = true
			}
			if isSlice {
				slices[name.Name] = true
			}
		}
	}
}

func registerRaterRegistrationNames(regs, slices map[string]bool, names []*ast.Ident, typ ast.Expr, values []ast.Expr) {
	if legacyTypeNamesRaterRegistration(typ) || legacyTypeNamesRaterRegistrationSlice(typ) {
		for _, name := range names {
			if name == nil {
				continue
			}
			if legacyTypeNamesRaterRegistration(typ) {
				regs[name.Name] = true
			}
			if legacyTypeNamesRaterRegistrationSlice(typ) {
				slices[name.Name] = true
			}
		}
		return
	}
	for i, name := range names {
		if name == nil || i >= len(values) {
			continue
		}
		if legacyExprConstructsRaterRegistration(values[i]) {
			regs[name.Name] = true
		}
		if legacyExprConstructsRaterRegistrationSlice(values[i]) {
			slices[name.Name] = true
		}
	}
}

func legacyExprConstructsRaterRegistration(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.CompositeLit:
		return legacyTypeNamesRaterRegistration(x.Type)
	case *ast.UnaryExpr:
		return x.Op == token.AND && legacyExprConstructsRaterRegistration(x.X)
	default:
		return false
	}
}

func legacyExprConstructsRaterRegistrationSlice(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.CompositeLit:
		return legacyTypeNamesRaterRegistrationSlice(x.Type)
	default:
		return false
	}
}

func legacyRangeOverRaterRegistrations(expr ast.Expr, slices map[string]bool) bool {
	switch x := expr.(type) {
	case *ast.Ident:
		return slices[x.Name]
	case *ast.SelectorExpr:
		return x.Sel != nil && x.Sel.Name == "RaterRegistrations"
	default:
		return legacyExprConstructsRaterRegistrationSlice(x)
	}
}

func legacyTypeNamesRaterRegistrationSlice(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.ArrayType:
		return legacyTypeNamesRaterRegistration(x.Elt)
	case *ast.StarExpr:
		return legacyTypeNamesRaterRegistrationSlice(x.X)
	case *ast.ParenExpr:
		return legacyTypeNamesRaterRegistrationSlice(x.X)
	default:
		return false
	}
}

func legacyExprInTypePosition(stack []ast.Node) bool {
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
			continue
		case *ast.FuncType:
			return false
		case *ast.InterfaceType, *ast.StructType:
			return false
		case *ast.CallExpr:
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
