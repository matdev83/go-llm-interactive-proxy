package lipruntime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Fixed current-major deprecated Options fields (Task 8.3 no-growth; Task 8.4 removes).
var migrationFixedDeprecatedOptionFields = []string{
	"RequestProviders",
	"AttemptProviders",
	"ConcurrencyProvider",
	"Rater",
	"ProviderDescriptors",
}

// Options fields the legacy adapter may read/write/clear (deprecated + mix conflicts).
var migrationAllowedAdapterOptionsSelectors = map[string]bool{
	"RequestProviders":        true,
	"AttemptProviders":        true,
	"ConcurrencyProvider":     true,
	"Rater":                   true,
	"ProviderDescriptors":     true,
	"RequestRegistrations":    true,
	"AttemptRegistrations":    true,
	"ConcurrencyRegistration": true,
	"RaterRegistrations":      true,
}

var migrationAllowedLegacyHelperFuncs = []string{
	"prepareCanonicalProduction",
	"adaptLegacyOptions",
	"filterDescriptorsByFamily",
	"descriptorHasFamily",
	"legacyRequestRegistrations",
	"legacyAttemptRegistrations",
	"legacyConcurrencyRegistration",
}

var migrationAllowedLegacyTypes = []string{
	"stageFamily",
}

var migrationAllowedLegacyConsts = []string{
	"stageFamilyRequest",
	"stageFamilyAttempt",
	"stageFamilyLease",
	"legacyProductionRaterID",
}

var migrationAllowedLegacyStageFamilies = []string{
	"stageFamilyRequest",
	"stageFamilyAttempt",
	"stageFamilyLease",
}

var migrationAllowedLegacyStages = map[string]bool{
	"StageRequestAdmit":   true,
	"StageRequestSettle":  true,
	"StageRequestRelease": true,
	"StageAttemptAdmit":   true,
	"StageAttemptSettle":  true,
	"StageAttemptRelease": true,
	"StageLeaseAdmit":     true,
	"StageLeaseRelease":   true,
}

func TestLegacyOptions_Migration_NoNewDeprecatedFields(t *testing.T) {
	t.Parallel()
	path := siblingPath(t, "options.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse options.go: %v", err)
	}
	deprecated := map[string]bool{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name == nil || ts.Name.Name != "Options" {
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
						deprecated[n.Name] = true
					}
				}
			}
		}
	}
	var got []string
	for name := range deprecated {
		got = append(got, name)
	}
	sort.Strings(got)
	want := append([]string(nil), migrationFixedDeprecatedOptionFields...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("deprecated Options field set drift: got %v want %v (no new current-major legacy fields)", got, want)
	}

	// Reflection cross-check: every fixed deprecated field remains exported on Options.
	typ := reflect.TypeOf(Options{})
	for _, name := range migrationFixedDeprecatedOptionFields {
		if _, ok := typ.FieldByName(name); !ok {
			t.Fatalf("Options missing exported deprecated field %q", name)
		}
	}
}

func TestLegacyOptions_Migration_AdapterReadsOnlyFixedDeprecatedFieldsAndCanonicalConflicts(t *testing.T) {
	t.Parallel()
	path := siblingPath(t, "legacy_options.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sels, err := collectLegacyAdapterOptionsSelectors(string(src), "legacy_options.go")
	if err != nil {
		t.Fatalf("collect selectors: %v", err)
	}
	var unexpected []string
	for name := range sels {
		if !migrationAllowedAdapterOptionsSelectors[name] {
			unexpected = append(unexpected, name)
		}
	}
	sort.Strings(unexpected)
	if len(unexpected) > 0 {
		t.Fatalf("legacy adapter touched Options fields outside fixed surface: %v (allowed=%v)",
			unexpected, sortedKeys(migrationAllowedAdapterOptionsSelectors))
	}
	// Require the deprecated + conflict surface still be exercised so the gate
	// cannot silently shrink by deleting adaptation.
	for _, want := range migrationFixedDeprecatedOptionFields {
		if !sels[want] {
			t.Fatalf("legacy adapter missing expected Options selector %q", want)
		}
	}
	for _, want := range []string{
		"RequestRegistrations",
		"AttemptRegistrations",
		"ConcurrencyRegistration",
		"RaterRegistrations",
	} {
		if !sels[want] {
			t.Fatalf("legacy adapter missing expected conflict/pass-through selector %q", want)
		}
	}
}

func TestLegacyOptions_Migration_AdapterHelpersFrozen(t *testing.T) {
	t.Parallel()
	path := siblingPath(t, "legacy_options.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse legacy_options.go: %v", err)
	}

	var funcs, types, consts []string
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil {
				t.Fatalf("legacy adapter must not grow methods; found %s", d.Name.Name)
			}
			if d.Name != nil && d.Name.Name != "" && d.Name.Name != "_" {
				funcs = append(funcs, d.Name.Name)
			}
		case *ast.GenDecl:
			switch d.Tok {
			case token.TYPE:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name == nil {
						continue
					}
					types = append(types, ts.Name.Name)
				}
			case token.CONST:
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, name := range vs.Names {
						if name != nil && name.Name != "_" {
							consts = append(consts, name.Name)
						}
					}
				}
			}
		}
	}

	assertExactSorted(t, "legacy helper funcs", funcs, migrationAllowedLegacyHelperFuncs)
	assertExactSorted(t, "legacy types", types, migrationAllowedLegacyTypes)
	assertExactSorted(t, "legacy consts", consts, migrationAllowedLegacyConsts)
}

func TestLegacyOptions_Migration_AdapterSelectorGate_Synthetic(t *testing.T) {
	t.Parallel()

	allowedSrc := `package lipruntime
func adaptLegacyOptions(opts Options) (Options, error) {
	_ = opts.ProviderDescriptors
	out := opts
	if len(opts.RequestProviders) > 0 && len(opts.RequestRegistrations) > 0 {
		return Options{}, nil
	}
	if len(opts.AttemptProviders) > 0 && len(opts.AttemptRegistrations) > 0 {
		return Options{}, nil
	}
	if opts.ConcurrencyProvider != nil && opts.ConcurrencyRegistration != nil {
		return Options{}, nil
	}
	if opts.Rater != nil && len(opts.RaterRegistrations) > 0 {
		return Options{}, nil
	}
	out.RequestRegistrations = nil
	out.AttemptRegistrations = nil
	out.ConcurrencyRegistration = nil
	out.RaterRegistrations = nil
	out.RequestProviders = nil
	out.AttemptProviders = nil
	out.ConcurrencyProvider = nil
	out.Rater = nil
	out.ProviderDescriptors = nil
	return out, nil
}
`
	got, err := collectLegacyAdapterOptionsSelectors(allowedSrc, "synthetic_ok.go")
	if err != nil {
		t.Fatalf("allowed synthetic parse: %v", err)
	}
	for name := range got {
		if !migrationAllowedAdapterOptionsSelectors[name] {
			t.Fatalf("allowed synthetic unexpectedly reported %q", name)
		}
	}

	cases := []struct {
		name string
		src  string
		bad  string
	}{
		{
			name: "undeclared_deprecated_field_read",
			src: `package lipruntime
func adaptLegacyOptions(opts Options) (Options, error) {
	if opts.ExperimentalLegacyProvider != nil {
		return opts, nil
	}
	return opts, nil
}
`,
			bad: "ExperimentalLegacyProvider",
		},
		{
			name: "build_only_field_read",
			src: `package lipruntime
func adaptLegacyOptions(opts Options) (Options, error) {
	_ = opts.TrafficObservers
	return opts, nil
}
`,
			bad: "TrafficObservers",
		},
		{
			name: "out_alias_write",
			src: `package lipruntime
func adaptLegacyOptions(opts Options) (Options, error) {
	out := opts
	out.LogWriter = nil
	return out, nil
}
`,
			bad: "LogWriter",
		},
		{
			name: "pointer_alias_read",
			src: `package lipruntime
func adaptLegacyOptions(opts Options) (Options, error) {
	p := &opts
	_ = p.MeteringRecorder
	return opts, nil
}
`,
			bad: "MeteringRecorder",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sels, err := collectLegacyAdapterOptionsSelectors(tc.src, "synthetic_bad.go")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !sels[tc.bad] {
				t.Fatalf("expected selector gate to observe unapproved field %q; got %v", tc.bad, sortedKeys(sels))
			}
			if migrationAllowedAdapterOptionsSelectors[tc.bad] {
				t.Fatalf("%q must remain outside allowed adapter selector set", tc.bad)
			}
		})
	}
}

func TestLegacyOptions_Migration_StageFamiliesFrozen(t *testing.T) {
	t.Parallel()
	path := siblingPath(t, "legacy_options.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse legacy_options.go: %v", err)
	}

	var families []string
	var stages []string
	ast.Inspect(file, func(n ast.Node) bool {
		gen, ok := n.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			return true
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range vs.Names {
				if strings.HasPrefix(name.Name, "stageFamily") {
					families = append(families, name.Name)
				}
			}
		}
		return true
	})
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok || x.Name != "authority" {
			return true
		}
		if strings.HasPrefix(sel.Sel.Name, "Stage") {
			stages = append(stages, sel.Sel.Name)
		}
		return true
	})

	sort.Strings(families)
	wantFamilies := append([]string(nil), migrationAllowedLegacyStageFamilies...)
	sort.Strings(wantFamilies)
	if strings.Join(families, ",") != strings.Join(wantFamilies, ",") {
		t.Fatalf("legacy stageFamily set must stay frozen: got %v want %v", families, wantFamilies)
	}

	seen := make(map[string]bool)
	var unexpected []string
	for _, s := range stages {
		if seen[s] {
			continue
		}
		seen[s] = true
		if !migrationAllowedLegacyStages[s] {
			unexpected = append(unexpected, s)
		}
	}
	if len(unexpected) > 0 {
		t.Fatalf("legacy adapter introduced new stage family/class via Stage refs: %v", unexpected)
	}
	for want := range migrationAllowedLegacyStages {
		if !seen[want] {
			t.Fatalf("legacy adapter missing expected stage %q (pairing surface must stay exact)", want)
		}
	}
}

func TestLegacyOptions_Migration_RaterCompatibilityOnly(t *testing.T) {
	t.Parallel()
	out, err := adaptLegacyOptions(Options{Rater: stubRater{}})
	if err != nil {
		t.Fatalf("adaptLegacyOptions: %v", err)
	}
	if len(out.RaterRegistrations) != 1 {
		t.Fatalf("regs=%d", len(out.RaterRegistrations))
	}
	reg := out.RaterRegistrations[0]
	if reg.ID != "legacy-production-rater" {
		t.Fatalf("id=%q (must remain compatibility-only legacy-production-rater)", reg.ID)
	}
	if reg.Perspective != metering.PerspectiveOperator {
		t.Fatalf("perspective=%q want operator only (no customer/provider inference)", reg.Perspective)
	}
	if reg.Perspective == metering.PerspectiveCustomer {
		t.Fatal("legacy Rater must not map to customer perspective")
	}
}

func TestLegacyOptions_Migration_CanonicalWithoutProviderDescriptors(t *testing.T) {
	t.Parallel()
	in := Options{
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: requestDesc("mig-req"),
			Priority:   authority.RequestPriorityQuotaBudgetRate,
			Provider:   allowReq{},
		}},
		AttemptRegistrations: []authority.AttemptRegistration{{
			Descriptor: attemptDesc("mig-att"),
			Priority:   authority.AttemptPriorityHardSpend,
			Provider:   allowAtt{},
		}},
		ConcurrencyRegistration: &authority.ConcurrencyRegistration{
			Descriptor: leaseDesc("mig-lease"),
			Provider:   allowConc{},
		},
		RaterRegistrations: []economics.RaterRegistration{{
			ID: "mig-rater", Perspective: metering.PerspectiveOperator, Rater: stubRater{},
		}},
	}
	if len(in.ProviderDescriptors) != 0 {
		t.Fatal("fixture must omit ProviderDescriptors")
	}
	norm, err := prepareCanonicalProduction(in)
	if err != nil {
		t.Fatalf("canonical path must pass without ProviderDescriptors: %v", err)
	}
	if len(norm.RequestRegistrations) != 1 || norm.RequestRegistrations[0].Descriptor.ID != "mig-req" {
		t.Fatalf("request=%+v", norm.RequestRegistrations)
	}
}

func TestLegacyOptions_Migration_UnknownStageDoesNotBind(t *testing.T) {
	t.Parallel()
	// A descriptor that declares only a non-family/unknown pairing surface must
	// not satisfy legacy request cardinality (no silent new provider class).
	_, err := adaptLegacyOptions(Options{
		RequestProviders: []authority.RequestProvider{allowReq{}},
		ProviderDescriptors: []authority.ProviderDescriptor{{
			ID:   "not-a-request-family",
			Kind: authority.ProviderKindAuthority,
			Postures: []authority.StagePosture{{
				Stage:           authority.StageLeaseAdmit,
				Strength:        authority.StrengthRequired,
				FailureBehavior: authority.FailureFailClosed,
			}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "matching request-stage ProviderDescriptors") {
		t.Fatalf("lease-only descriptor must not pair as request provider class: err=%v", err)
	}
}

// collectLegacyAdapterOptionsSelectors returns Options field names selected on
// Options-typed parameters/locals in adaptLegacyOptions (and aliases such as
// out := opts). Whole-struct copies do not count as field selectors.
func collectLegacyAdapterOptionsSelectors(src, filename string) (map[string]bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Name == nil {
			continue
		}
		bound := map[string]bool{}
		if fd.Type != nil && fd.Type.Params != nil {
			for _, field := range fd.Type.Params.List {
				if !isOptionsType(field.Type) {
					continue
				}
				for _, n := range field.Names {
					if n != nil {
						bound[n.Name] = true
					}
				}
			}
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.DeclStmt:
				gd, ok := node.Decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					return true
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					typed := isOptionsType(vs.Type)
					for i, name := range vs.Names {
						if name == nil || name.Name == "_" {
							continue
						}
						if typed {
							bound[name.Name] = true
							continue
						}
						if len(vs.Values) == 0 || i >= len(vs.Values) {
							continue
						}
						rhs := vs.Values[i]
						if isOptionsType(rhs) {
							bound[name.Name] = true
							continue
						}
						if _, ok := optionsRootIdent(rhs, bound); ok {
							bound[name.Name] = true
						}
					}
				}
			case *ast.AssignStmt:
				for i, lhs := range node.Lhs {
					ident, ok := lhs.(*ast.Ident)
					if !ok || ident.Name == "_" {
						continue
					}
					var rhs ast.Expr
					switch {
					case len(node.Rhs) == len(node.Lhs):
						rhs = node.Rhs[i]
					case len(node.Rhs) == 1:
						rhs = node.Rhs[0]
					default:
						continue
					}
					if isOptionsType(rhs) {
						bound[ident.Name] = true
						continue
					}
					if _, ok := optionsRootIdent(rhs, bound); ok {
						bound[ident.Name] = true
					}
				}
			case *ast.SelectorExpr:
				if node.Sel == nil {
					return true
				}
				if _, ok := optionsRootIdent(node.X, bound); ok {
					out[node.Sel.Name] = true
				}
			}
			return true
		})
	}
	return out, nil
}

// optionsRootIdent reports whether expr ultimately names an Options-bound ident,
// allowing &opts, *p, and parentheses without treating unrelated selectors as Options.
func optionsRootIdent(expr ast.Expr, bound map[string]bool) (string, bool) {
	switch e := expr.(type) {
	case *ast.Ident:
		if bound[e.Name] {
			return e.Name, true
		}
	case *ast.ParenExpr:
		return optionsRootIdent(e.X, bound)
	case *ast.StarExpr:
		return optionsRootIdent(e.X, bound)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return optionsRootIdent(e.X, bound)
		}
	}
	return "", false
}

func isOptionsType(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "Options"
	case *ast.SelectorExpr:
		return e.Sel != nil && e.Sel.Name == "Options"
	case *ast.CompositeLit:
		return isOptionsType(e.Type)
	case *ast.UnaryExpr:
		return e.Op == token.AND && isOptionsType(e.X)
	case *ast.StarExpr:
		return isOptionsType(e.X)
	case *ast.CallExpr:
		if id, ok := e.Fun.(*ast.Ident); ok && id.Name == "Options" {
			return true
		}
	}
	return false
}

func assertExactSorted(t *testing.T, label string, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if strings.Join(g, ",") != strings.Join(w, ",") {
		t.Fatalf("%s drift: got %v want %v", label, g, w)
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func siblingPath(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), name)
}
