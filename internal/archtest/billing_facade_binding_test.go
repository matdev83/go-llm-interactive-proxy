package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Task 15.2 certification: the explicit typed billing binding is the single
// monetary entrypoint on the public facade. Ordinary Options and stock
// startup stay non-money (see TestPhase1PublicOptionsStayNonMonetary); this
// gate names the one exception and forbids any second one.

// TestBuildWithBillingIsTheSingleMonetaryEntrypoint proves exactly one
// exported pkg/lipruntime function touches the typed billing binding, that it
// is named BuildWithBilling, and that stock lipstd startup never calls it.
func TestBuildWithBillingIsTheSingleMonetaryEntrypoint(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "pkg", "lipruntime")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	referencing := map[string]bool{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		billingLocal := ""
		for _, imp := range file.Imports {
			impPath := strings.Trim(imp.Path.Value, `"`)
			if strings.HasSuffix(impPath, "/pkg/lipsdk/billing") {
				if imp.Name != nil {
					billingLocal = imp.Name.Name
				} else {
					billingLocal = "billing"
				}
			}
		}
		if billingLocal == "" {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || !ast.IsExported(fn.Name.Name) || fn.Body == nil {
				continue
			}
			touches := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if ok && id.Name == billingLocal {
					touches = true
					return false
				}
				return true
			})
			ast.Inspect(fn.Type, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if ok && id.Name == billingLocal {
					touches = true
					return false
				}
				return true
			})
			if touches {
				referencing[fn.Name.Name] = true
			}
		}
	}
	if len(referencing) != 1 || !referencing["BuildWithBilling"] {
		t.Fatalf("monetary entrypoints = %v, want exactly [BuildWithBilling]", referencing)
	}
	lipstd, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(lipstdCommandPath)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(lipstd), "BuildWithBilling") {
		t.Fatal("stock lipstd startup must remain non-money: must not call BuildWithBilling")
	}
}

// TestBillingBindingAdapterHoldsNoAdmissionMath proves the Task 15.2
// delegating adapter implements no quote/settle/balance math of its own: the
// single monetary admission authority keeps the math, the adapter only
// translates DTOs onto the validated public binding ports.
func TestBillingBindingAdapterHoldsNoAdmissionMath(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash("internal/infra/billingbinding/adapter.go")))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, term := range []string{
		"EstimateMaxCustomerCharge",
		"EstimateRichCustomerCharge",
		"EvaluateSettle",
		"EvaluateAdmit",
		"ApplyBalanceDelta",
		"AdmitExposure",
	} {
		if strings.Contains(body, term) {
			t.Fatalf("billingbinding adapter must not contain admission math (%q); it delegates to binding ports", term)
		}
	}
	for _, delegation := range []string{
		"binding.CreditScreen.Check",
		"binding.Quoter.Quote",
		"binding.Admission.Admit",
		"binding.Terminal.AppendTerminal",
	} {
		if !strings.Contains(body, delegation) {
			t.Fatalf("billingbinding adapter must delegate through %q", delegation)
		}
	}
}

// TestFacadeConvergesOnOneBuildHost proves Build and BuildWithBilling share a
// single BuildHost invocation in the public facade: no second host, copied
// runtime stack, or forked reload path.
func TestFacadeConvergesOnOneBuildHost(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "pkg", "lipruntime")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	sites := 0
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == "BuildHost" {
				sites++
			}
			return true
		})
	}
	if sites != 1 {
		t.Fatalf("pkg/lipruntime BuildHost call sites = %d, want exactly 1 shared assembly", sites)
	}
}
