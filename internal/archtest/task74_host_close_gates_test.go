package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Task 7.4 production gates: Host.Close is the unique production coordinator
// of manager shutdown, process-runtime close, and tracing shutdown ordering.

const hcHostOwnerDir = "internal/infra/runtimebundle"

// hcProductionPackages parses every production (non-test) Go file grouped by
// repo-relative directory. External test packages and _test.go files are
// excluded, so test-only fakes never participate in the production gate.
func hcProductionPackages(t *testing.T, root string) map[string]map[string]*ast.File {
	t.Helper()
	out := map[string]map[string]*ast.File{}
	err := walkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, abs, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		if out[dir] == nil {
			out[dir] = map[string]*ast.File{}
		}
		out[dir][filepath.ToSlash(rel)] = f
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestHostCloseOwnership_ProductionTargetIsGreen proves no production package
// outside the canonical Host.Close call graph reaches a Host-owned manager
// shutdown, process close, coordinator shutdown, or tracing shutdown primitive.
func TestHostCloseOwnership_ProductionTargetIsGreen(t *testing.T) {
	t.Parallel()
	pkgs := hcProductionPackages(t, repoRoot(t))
	var all []hcFinding
	for _, dir := range hcSortedKeys(pkgs) {
		zone := hcZone{Scope: dir, HostOwner: dir == hcHostOwnerDir}
		all = append(all, analyzeHostCloseOwnership(zone, pkgs[dir])...)
	}
	if len(all) != 0 {
		lines := make([]string, 0, len(all))
		for _, f := range all {
			lines = append(lines, f.String())
		}
		t.Fatalf("Host shutdown ownership violations (%d):\n%s", len(all), strings.Join(lines, "\n"))
	}
}

// TestHostCloseOwnership_SolePreHostRollbackOwner proves runtimebundle keeps
// exactly one shape-based pre-Host rollback owner for resources acquired before
// a complete Host exists.
func TestHostCloseOwnership_SolePreHostRollbackOwner(t *testing.T) {
	t.Parallel()
	pkgs := hcProductionPackages(t, repoRoot(t))
	owner, ok := pkgs[hcHostOwnerDir]
	if !ok {
		t.Fatalf("missing production package %s", hcHostOwnerDir)
	}
	idx := hcBuildIndex(hcZone{Scope: hcHostOwnerDir, HostOwner: true}, owner)
	found := map[string]bool{}
	for _, name := range hcSortedKeys(idx.funcs) {
		if idx.hcIsPreHostRollbackShape(idx.funcs[name]) {
			found[name] = true
		}
	}
	if len(found) != 1 || !found["joinInitialFailureCleanup"] {
		t.Fatalf("pre-Host rollback owners=%v want exactly [joinInitialFailureCleanup]", hcSortedKeys(found))
	}
}

// TestHostCloseOwnership_SoleProductionCoordinator proves exactly one
// production entry point drives the Host shutdown ordering and that it is
// (*ReloadHost).Close.
func TestHostCloseOwnership_SoleProductionCoordinator(t *testing.T) {
	t.Parallel()
	pkgs := hcProductionPackages(t, repoRoot(t))
	owner, ok := pkgs[hcHostOwnerDir]
	if !ok {
		t.Fatalf("missing production package %s", hcHostOwnerDir)
	}
	rep := reportHostCloseOwnership(hcZone{Scope: hcHostOwnerDir, HostOwner: true}, owner)
	roots := rep.Roots()
	if len(roots) != 1 || roots[0] != "ReloadHost.Close" {
		t.Fatalf("Host shutdown ordering roots=%v want exactly [ReloadHost.Close]", roots)
	}
	kinds := map[string]bool{}
	for touched, set := range rep.Touching {
		if !rep.Blessed[touched] {
			t.Fatalf("owner %s reaches Host shutdown primitives outside the Host.Close call graph", touched)
		}
		for _, k := range set {
			kinds[k] = true
		}
	}
	for _, want := range []string{"manager", "process", "tracing", "coordinator"} {
		if !kinds[want] {
			t.Fatalf("Host.Close call graph must own the %s shutdown phase; reached kinds=%v", want, hcSortedKeys(kinds))
		}
	}
}

// TestHostCloseOwnership_ServeInputCarriesOnlyTheHostSeam proves the stdhttp
// serve input cannot carry Manager, ProcessServices, Coordinator, tracing
// shutdown, or any renamed/nested lifecycle callback bag.
func TestHostCloseOwnership_ServeInputCarriesOnlyTheHostSeam(t *testing.T) {
	t.Parallel()
	pkgs := hcProductionPackages(t, repoRoot(t))
	stdhttpPkg, ok := pkgs["internal/stdhttp"]
	if !ok {
		t.Fatal("missing production package internal/stdhttp")
	}
	findings := analyzeServeInputShape(stdhttpPkg)
	if len(findings) != 0 {
		lines := make([]string, 0, len(findings))
		for _, f := range findings {
			lines = append(lines, f.String())
		}
		t.Fatalf("serve input shape violations (%d):\n%s", len(findings), strings.Join(lines, "\n"))
	}
}

// TestHostCloseOwnership_ServeAdapterDropsOwnershipImports proves the serve
// adapter no longer imports the host/generation ownership packages.
func TestHostCloseOwnership_ServeAdapterDropsOwnershipImports(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "stdhttp", "generation_host.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range f.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		if p == importRuntimebundle || p == importRuntimehost {
			t.Fatalf("generation_host.go must not import %s for shutdown ownership", p)
		}
	}
}

// TestHostCloseOwnership_DeletedDuplicateOrchestration proves the duplicated
// CLI/HTTP shutdown orchestration stays deleted.
func TestHostCloseOwnership_DeletedDuplicateOrchestration(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	gone := map[string][]string{
		"internal/stdhttp/generation_host.go": {"shutdownGenerationHost", "closeProcessServices"},
		"cmd/lipstd/command.go":               {"tracingDeferred", "deferHostTracingShutdown"},
	}
	for rel, symbols := range gone {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		for _, sym := range symbols {
			if strings.Contains(string(src), sym) {
				t.Fatalf("%s must not reference deleted shutdown orchestration %s", rel, sym)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, "cmd", "lipstd", "tracing_shutdown.go")); err == nil {
		t.Fatal("cmd/lipstd/tracing_shutdown.go must be deleted")
	}
}

// --- serve input shape analyzer ---

// analyzeServeInputShape resolves the sole exported serve entry point's input
// struct and rejects any field that is not one of the allowed focused shapes.
func analyzeServeInputShape(files map[string]*ast.File) []hcFinding {
	structs := map[string]*ast.StructType{}
	interfaces := map[string]*ast.InterfaceType{}
	aliases := map[string]string{}
	var entry *ast.FuncDecl
	for _, name := range hcSortedKeys(files) {
		for _, decl := range files[name].Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name == nil {
						continue
					}
					switch t := ts.Type.(type) {
					case *ast.StructType:
						structs[ts.Name.Name] = t
					case *ast.InterfaceType:
						interfaces[ts.Name.Name] = t
					default:
						aliases[ts.Name.Name] = hcTypeString(ts.Type)
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name != nil && d.Name.Name == "RunWithGenerationHost" {
					entry = d
				}
			}
		}
	}
	if entry == nil || entry.Type == nil || entry.Type.Params == nil {
		return []hcFinding{{Scope: "internal/stdhttp", Owner: "RunWithGenerationHost", Detail: "missing sole serve entry point"}}
	}
	var inputType string
	for _, f := range entry.Type.Params.List {
		typ := hcTypeString(f.Type)
		if typ == "context.Context" {
			continue
		}
		inputType = strings.TrimPrefix(typ, "*")
	}
	st, ok := structs[inputType]
	if !ok {
		if target, ok := aliases[inputType]; ok {
			st, ok = structs[strings.TrimPrefix(target, "*")]
			if !ok {
				return []hcFinding{{Scope: "internal/stdhttp", Owner: inputType, Detail: "serve input type is not a resolvable struct"}}
			}
		} else {
			return []hcFinding{{Scope: "internal/stdhttp", Owner: inputType, Detail: "serve input type is not a resolvable struct"}}
		}
	}
	return checkServeInputStruct(inputType, st, structs, interfaces, aliases, map[string]bool{})
}

// hcAllowedServeFieldTypes are the focused non-lifecycle inputs the serve
// adapter genuinely needs.
var hcAllowedServeFieldTypes = map[string]bool{
	"*config.Config": true,
	"*slog.Logger":   true,
	"time.Duration":  true,
}

// hcHostSeamMethods is the canonical host-facing serve seam: a stable data
// plane handler, early trigger rejection, and the canonical host close.
var hcHostSeamMethods = map[string]string{
	"HTTPHandler":   "()(http.Handler)",
	"BeginShutdown": "()()",
	"Close":         "(context.Context)(error)",
}

// hcManagementMethods is the process-owned management listener seam.
var hcManagementMethods = map[string]string{
	"Shutdown": "(context.Context)(error)",
}

func checkServeInputStruct(
	name string,
	st *ast.StructType,
	structs map[string]*ast.StructType,
	interfaces map[string]*ast.InterfaceType,
	aliases map[string]string,
	visiting map[string]bool,
) []hcFinding {
	var out []hcFinding
	if st == nil || st.Fields == nil || visiting[name] {
		return out
	}
	visiting[name] = true
	for _, f := range st.Fields.List {
		field := "embedded"
		if len(f.Names) > 0 && f.Names[0] != nil {
			field = f.Names[0].Name
		}
		typ := hcTypeString(f.Type)
		if hcAllowedServeFieldTypes[typ] {
			continue
		}
		methods := hcMethodSet(f.Type, interfaces, aliases, map[string]bool{})
		if methods != nil {
			if hcMethodSetEquals(methods, hcHostSeamMethods) || hcMethodSetEquals(methods, hcManagementMethods) {
				continue
			}
			out = append(out, hcFinding{
				Scope:  "internal/stdhttp",
				Owner:  name + "." + field,
				Detail: "serve input field carries a non-canonical lifecycle interface " + hcFormatMethodSet(methods),
			})
			continue
		}
		out = append(out, hcFinding{
			Scope:  "internal/stdhttp",
			Owner:  name + "." + field,
			Detail: "serve input field of type " + typ + " is not one of the focused serve inputs",
		})
	}
	return out
}

// hcMethodSet returns the method set of an interface type (inline, named, or
// embedded), or nil when the expression is not an interface.
func hcMethodSet(expr ast.Expr, interfaces map[string]*ast.InterfaceType, aliases map[string]string, visiting map[string]bool) map[string]string {
	switch t := expr.(type) {
	case *ast.InterfaceType:
		out := map[string]string{}
		if t.Methods == nil {
			return out
		}
		for _, m := range t.Methods.List {
			if len(m.Names) > 0 {
				ft, ok := m.Type.(*ast.FuncType)
				if !ok {
					continue
				}
				out[m.Names[0].Name] = hcFuncSignature(ft)
				continue
			}
			for k, v := range hcMethodSet(m.Type, interfaces, aliases, visiting) {
				out[k] = v
			}
		}
		return out
	case *ast.StarExpr:
		return hcMethodSet(t.X, interfaces, aliases, visiting)
	case *ast.Ident:
		if visiting[t.Name] {
			return nil
		}
		visiting[t.Name] = true
		if iface, ok := interfaces[t.Name]; ok {
			return hcMethodSet(iface, interfaces, aliases, visiting)
		}
		if target, ok := aliases[t.Name]; ok {
			return hcMethodSet(ast.NewIdent(strings.TrimPrefix(target, "*")), interfaces, aliases, visiting)
		}
	}
	return nil
}

func hcMethodSetEquals(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for name, sig := range want {
		if got[name] != sig {
			return false
		}
	}
	return true
}

func hcFormatMethodSet(methods map[string]string) string {
	names := make([]string, 0, len(methods))
	for k, v := range methods {
		names = append(names, k+v)
	}
	sort.Strings(names)
	return "{" + strings.Join(names, ";") + "}"
}
