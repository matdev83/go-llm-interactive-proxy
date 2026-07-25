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

const (
	lipruntimeDir              = "pkg/lipruntime"
	lipruntimeBuildPath        = "pkg/lipruntime/build.go"
	lipruntimeHostPath         = "pkg/lipruntime/host.go"
	lipruntimeFacadePath       = "pkg/lipruntime/facade.go"
	lipruntimeFinalLineCeiling = 150
	lipruntimePackageCeiling   = 486 // Phase B+C facade pass-through retirement
	lipruntimeAdapterTypeName  = "bundleHost"
	lipruntimeHostIfaceName    = "hostAPI"
)

func rtForbiddenFields() map[string]bool {
	return map[string]bool{
		"Manager": true, "Process": true, "ProcessRuntime": true, "ProcessServices": true,
		"Coordinator": true, "Source": true, "ShutdownTracing": true,
		"executor": true, "reload": true,
		"trafficObserversAttached": true, "usageObserversAttached": true,
		"evidenceSinkAttached": true, "raterAttached": true,
		"meteringAttached": true, "meteringQuerierAttached": true,
		"GenerationOwner": true, "SnapshotGeneration": true, "SnapshotController": true,
	}
}

var rtAllowedSyncFieldNames = map[string]bool{"closeMu": true, "closed": true}

// Forbidden shutdown/ownership coordination selectors anywhere in pkg/lipruntime.
var rtForbiddenShutdownSelectors = map[string]bool{
	"ShutdownDetached": true,
	"RetireGeneration": true,
	"BeginShutdown":    true,
	"WaitForIdle":      true,
}

// Contested concrete ownership type base names (including common aliases).
var rtContestedTypeNames = map[string]bool{
	"Host": true, "ReloadHost": true, "Manager": true, "ProcessServices": true,
	"Coordinator": true, "ProcessRuntime": true,
}

type rtFinding struct {
	File   string
	Owner  string
	Detail string
}

func (f rtFinding) String() string {
	return fmt.Sprintf("%s: %s: %s", f.File, f.Owner, f.Detail)
}

func TestRuntimeFacade_OneHostDependencyShape(t *testing.T) {
	t.Parallel()
	files := rtProductionFiles(t)
	findings := analyzeRuntimeStructShape(files)
	if len(findings) != 0 {
		t.Fatalf("Runtime host-dependency shape violations (%d):\n%s", len(findings), joinRTFindings(findings))
	}
}

func TestRuntimeFacade_NoOwnershipPrimitivesOutsideAdapter(t *testing.T) {
	t.Parallel()
	files := rtProductionFiles(t)
	findings := analyzeRuntimeOwnershipReach(files)
	if len(findings) != 0 {
		t.Fatalf("Runtime ownership reach violations (%d):\n%s", len(findings), joinRTFindings(findings))
	}
}

func TestRuntimeFacade_BuildFileBudgetAtMost150(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	n, err := countFileLines(filepath.Join(root, lipruntimeBuildPath))
	if err != nil {
		t.Fatal(err)
	}
	if n > lipruntimeFinalLineCeiling {
		t.Fatalf("%s has %d lines; public build/facade assembly ceiling is %d", lipruntimeBuildPath, n, lipruntimeFinalLineCeiling)
	}
	var budgetMax int
	found := false
	for _, b := range CriticalFileBudgets {
		if b.Path == lipruntimeBuildPath {
			budgetMax = b.Max
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("CriticalFileBudgets missing %s", lipruntimeBuildPath)
	}
	if budgetMax > lipruntimeFinalLineCeiling {
		t.Fatalf("CriticalFileBudgets.Max=%d exceeds final ceiling %d", budgetMax, lipruntimeFinalLineCeiling)
	}
	if n != budgetMax {
		t.Fatalf("%s measured %d lines; CriticalFileBudgets.Max=%d (exact ratchet, no headroom)", lipruntimeBuildPath, n, budgetMax)
	}
}

func TestRuntimeFacade_PackageTreeBudgetExact(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	n, err := CountNonTestGoLines(filepath.Join(root, lipruntimeDir))
	if err != nil {
		t.Fatal(err)
	}
	if n > lipruntimePackageCeiling {
		t.Fatalf("%s has %d non-test lines; Task 8.4 ceiling is %d", lipruntimeDir, n, lipruntimePackageCeiling)
	}
	var budgetMax int
	found := false
	for _, b := range PackageTreeBudgets {
		if b.Tree == lipruntimeDir {
			budgetMax = b.Max
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("PackageTreeBudgets missing %s", lipruntimeDir)
	}
	if n != budgetMax {
		t.Fatalf("%s measured %d; PackageTreeBudgets.Max=%d (exact, no headroom)", lipruntimeDir, n, budgetMax)
	}
	for _, path := range []string{lipruntimeHostPath, lipruntimeFacadePath} {
		fn, err := countFileLines(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		var fileMax int
		fileFound := false
		for _, b := range CriticalFileBudgets {
			if b.Path == path {
				fileMax = b.Max
				fileFound = true
				break
			}
		}
		if !fileFound {
			t.Fatalf("CriticalFileBudgets missing %s", path)
		}
		if fn != fileMax {
			t.Fatalf("%s measured %d; CriticalFileBudgets.Max=%d (exact)", path, fn, fileMax)
		}
	}
}

func TestRuntimeFacade_DetectorCatchesEvasions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		files   map[string]string
		wantSub string
	}{
		{
			name: "concrete_host_field_on_runtime",
			files: map[string]string{"build.go": `package lipruntime
type Host struct{}
type Runtime struct { host *Host; closeMu struct{}; closed bool }
`},
			wantSub: "concrete host type",
		},
		{
			name: "build_time_boolean",
			files: map[string]string{"build.go": `package lipruntime
type hostAPI interface{}
type Runtime struct { host hostAPI; trafficObserversAttached bool }
`},
			wantSub: "forbidden field trafficObserversAttached",
		},
		{
			name: "manager_field",
			files: map[string]string{"build.go": `package lipruntime
type hostAPI interface{}
type Manager struct{}
type Runtime struct { host hostAPI; Manager *Manager }
`},
			wantSub: "forbidden field Manager",
		},
		{
			name: "embedded_manager",
			files: map[string]string{"build.go": `package lipruntime
type hostAPI interface{}
type Manager struct{}
type Runtime struct { host hostAPI; *Manager }
`},
			wantSub: "forbidden field Manager",
		},
		{
			name: "nested_holder",
			files: map[string]string{"build.go": `package lipruntime
type hostAPI interface{}
type ProcessServices struct{}
type bag struct{ Process *ProcessServices }
type Runtime struct { host hostAPI; inner bag }
`},
			wantSub: "nested ownership",
		},
		{
			name: "extra_executor_field",
			files: map[string]string{"build.go": `package lipruntime
type hostAPI interface{}
type ExecutorView interface{}
type Runtime struct { host hostAPI; executor ExecutorView }
`},
			wantSub: "forbidden field executor",
		},
		{
			name: "rogue_file_calls_shutdown",
			files: map[string]string{
				"host.go": rtCanonicalHostSrc,
				"rogue.go": `package lipruntime
type Manager struct{}
func (m *Manager) ShutdownDetached() {}
func sneak(m *Manager) { m.ShutdownDetached() }
`,
			},
			wantSub: "ShutdownDetached",
		},
		{
			name: "global_host",
			files: map[string]string{
				"host.go": rtCanonicalHostSrc,
				"globals.go": `package lipruntime
var leakedHost *Host
`,
			},
			wantSub: "package global retains contested type",
		},
		{
			name: "alias_host_global",
			files: map[string]string{
				"host.go": rtCanonicalHostSrc,
				"alias.go": `package lipruntime
type Bundle = Host
var stash Bundle
`,
			},
			wantSub: "package global retains contested type",
		},
		{
			name: "method_value_shutdown",
			files: map[string]string{
				"host.go": rtCanonicalHostSrc,
				"cb.go": `package lipruntime
type Manager struct{}
func (m *Manager) ShutdownDetached() {}
func stash(m *Manager) func() { return m.ShutdownDetached }
`,
			},
			wantSub: "ShutdownDetached",
		},
		{
			name: "callback_registration",
			files: map[string]string{
				"host.go": rtCanonicalHostSrc,
				"cb.go": `package lipruntime
type Manager struct{}
func (m *Manager) BeginShutdown() {}
func register(fn func()) {}
func wire(m *Manager) { register(m.BeginShutdown) }
`,
			},
			wantSub: "BeginShutdown",
		},
		{
			name: "factory_returns_manager",
			files: map[string]string{
				"host.go": rtCanonicalHostSrc,
				"factory.go": `package lipruntime
type Manager struct{}
func newManager() *Manager { return &Manager{} }
`,
			},
			wantSub: "factory returns contested type",
		},
		{
			name: "slice_storage",
			files: map[string]string{
				"host.go": rtCanonicalHostSrc,
				"store.go": `package lipruntime
type Manager struct{}
type hold struct{ ms []*Manager }
`,
			},
			wantSub: "contested type storage",
		},
		{
			name: "map_storage",
			files: map[string]string{
				"host.go": rtCanonicalHostSrc,
				"store.go": `package lipruntime
type ProcessServices struct{}
type hold struct{ byID map[string]*ProcessServices }
`,
			},
			wantSub: "contested type storage",
		},
		{
			name: "direct_forbidden_in_adapter",
			files: map[string]string{
				"host.go": `package lipruntime
type Host struct{ Manager *Manager; Process *ProcessServices; Coordinator *Coordinator }
type Manager struct{}
func (m *Manager) ShutdownDetached() {}
type ProcessServices struct{}
func (p *ProcessServices) Close() {}
type Coordinator struct{}
func (c *Coordinator) BeginShutdown() {}
type hostAPI interface{ Close() error }
type bundleHost struct{ h *Host }
func (b bundleHost) Close() error {
	b.h.Manager.ShutdownDetached()
	b.h.Process.Close()
	b.h.Coordinator.BeginShutdown()
	return nil
}
type Runtime struct{ host hostAPI }
`,
			},
			wantSub: "ShutdownDetached",
		},
		{
			name: "local_process_close_alias",
			files: map[string]string{
				"host.go": rtCanonicalHostSrc,
				"close.go": `package lipruntime
func bad(h *Host) { p := h.Process; p.Close() }
`,
			},
			wantSub: "Process.Close",
		},
		{
			name: "process_param_close",
			files: map[string]string{
				"host.go": rtCanonicalHostSrc,
				"close.go": `package lipruntime
func bad(p *ProcessServices) { p.Close() }
func (p *ProcessServices) Close() {}
`,
			},
			wantSub: "Process.Close",
		},
		{
			name: "helper_indirection",
			files: map[string]string{
				"host.go": rtCanonicalHostSrc,
				"helper.go": `package lipruntime
type Manager struct{}
func (m *Manager) RetireGeneration() {}
func helper(m *Manager) { m.RetireGeneration() }
func (r *Runtime) boom(m *Manager) { helper(m) }
`,
			},
			wantSub: "RetireGeneration",
		},
		{
			name: "tracing_shutdown_method_value",
			files: map[string]string{
				"host.go": rtCanonicalHostSrc,
				"trace.go": `package lipruntime
func steal(h *Host) any { return h.ShutdownTracing }
`,
			},
			wantSub: "ShutdownTracing",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := mustParseRTFiles(t, tc.files)
			got := append(analyzeRuntimeStructShape(files), analyzeRuntimeOwnershipReach(files)...)
			if len(got) == 0 {
				t.Fatal("expected detector findings")
			}
			joined := joinRTFindings(got)
			if !strings.Contains(joined, tc.wantSub) {
				t.Fatalf("findings=%q want substring %q", joined, tc.wantSub)
			}
		})
	}
}

const rtCanonicalHostSrc = `package lipruntime
import "sync"
type Host struct {
	Manager *Manager
	Process *ProcessServices
	Coordinator *Coordinator
	Executor interface{}
	ShutdownTracing func() error
}
type Manager struct{}
type ProcessServices struct{}
type Coordinator struct{}
type hostAPI interface {
	Ready() bool
	Close() error
}
type bundleHost struct{ h *Host }
func adaptHost(h *Host) (hostAPI, error) {
	if h == nil || h.Manager == nil || h.Process == nil || h.Executor == nil {
		return nil, nil
	}
	return bundleHost{h: h}, nil
}
func (b bundleHost) Ready() bool { return b.h != nil }
func (b bundleHost) Close() error {
	if b.h == nil { return nil }
	return b.h.Close()
}
func (h *Host) Close() error { return nil }
type Runtime struct {
	host hostAPI
	closeMu sync.Mutex
	closed bool
}
func (r *Runtime) Ready() bool { return r != nil && r.host != nil && r.host.Ready() }
`

func TestRuntimeFacade_DetectorAllowsCanonicalShape(t *testing.T) {
	t.Parallel()
	files := mustParseRTFiles(t, map[string]string{"host.go": rtCanonicalHostSrc})
	if got := append(analyzeRuntimeStructShape(files), analyzeRuntimeOwnershipReach(files)...); len(got) != 0 {
		t.Fatalf("canonical shape must pass; got %v", got)
	}
}

func TestRuntimeFacade_UnrelatedNegativesIgnored(t *testing.T) {
	t.Parallel()
	files := mustParseRTFiles(t, map[string]string{
		"host.go": rtCanonicalHostSrc,
		"helpers.go": `package lipruntime
type Helper struct { Name string }
func (h *Helper) Close() error { return nil }
func normalize(s string) string { return s }
`,
	})
	if got := append(analyzeRuntimeStructShape(files), analyzeRuntimeOwnershipReach(files)...); len(got) != 0 {
		t.Fatalf("unrelated Helper must not fail: %v", got)
	}
}

func mustParseRTFiles(t *testing.T, sources map[string]string) map[string]*ast.File {
	t.Helper()
	out := map[string]*ast.File{}
	fset := token.NewFileSet()
	for name, src := range sources {
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		out[name] = f
	}
	return out
}

func rtProductionFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	root := repoRoot(t)
	out := map[string]*ast.File{}
	err := walkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		if filepath.ToSlash(filepath.Dir(rel)) != lipruntimeDir {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, abs, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		out[filepath.Base(rel)] = f
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("no production lipruntime files")
	}
	return out
}

func analyzeRuntimeStructShape(files map[string]*ast.File) []rtFinding {
	forbidden := rtForbiddenFields()
	var out []rtFinding
	runtimeDecls := 0
	for name, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil || ts.Name.Name != "Runtime" {
					continue
				}
				runtimeDecls++
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					out = append(out, rtFinding{File: name, Owner: "Runtime", Detail: "Runtime must be a struct"})
					continue
				}
				hostFields := 0
				for _, field := range st.Fields.List {
					fieldName := "embedded"
					if len(field.Names) > 0 && field.Names[0] != nil {
						fieldName = field.Names[0].Name
					} else {
						fieldName = rtTypeBaseName(field.Type)
					}
					if forbidden[fieldName] {
						out = append(out, rtFinding{File: name, Owner: "Runtime." + fieldName, Detail: "forbidden field " + fieldName})
						continue
					}
					if rtAllowedSyncFieldNames[fieldName] {
						continue
					}
					if fieldName == "host" || fieldName == lipruntimeHostIfaceName {
						hostFields++
						if !rtIsInterfaceType(field.Type, files) {
							out = append(out, rtFinding{File: name, Owner: "Runtime." + fieldName, Detail: "host field must be an interface (not concrete host type)"})
						}
						continue
					}
					if nested := rtNestedOwnership(field.Type, files, map[string]bool{}); nested != "" {
						out = append(out, rtFinding{File: name, Owner: "Runtime." + fieldName, Detail: "nested ownership " + nested})
						continue
					}
					out = append(out, rtFinding{File: name, Owner: "Runtime." + fieldName, Detail: "unexpected Runtime field " + fieldName})
				}
				if hostFields != 1 {
					out = append(out, rtFinding{File: name, Owner: "Runtime", Detail: fmt.Sprintf("want exactly one host-facing dependency field, found %d", hostFields)})
				}
			}
		}
	}
	if runtimeDecls > 1 {
		out = append(out, rtFinding{File: "Runtime", Owner: "Runtime", Detail: fmt.Sprintf("duplicate/ambiguous Runtime declarations: %d", runtimeDecls)})
	}
	return out
}

func analyzeRuntimeOwnershipReach(files map[string]*ast.File) []rtFinding {
	idx := buildRTIndex(files)
	var out []rtFinding
	out = append(out, idx.findAdapterShape()...)
	out = append(out, idx.findPackageGlobals()...)
	out = append(out, idx.findContestedStorage()...)
	out = append(out, idx.findFactories()...)
	out = append(out, idx.findForbiddenSelectors()...)
	return out
}

type rtIndex struct {
	files   map[string]*ast.File
	aliases map[string]string // name -> resolved base
	structs map[string]*ast.StructType
}

func buildRTIndex(files map[string]*ast.File) *rtIndex {
	idx := &rtIndex{
		files:   files,
		aliases: map[string]string{},
		structs: map[string]*ast.StructType{},
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil {
					continue
				}
				name := ts.Name.Name
				switch t := ts.Type.(type) {
				case *ast.StructType:
					idx.structs[name] = t
					idx.aliases[name] = name
				case *ast.Ident:
					idx.aliases[name] = t.Name
				case *ast.SelectorExpr:
					idx.aliases[name] = t.Sel.Name
				case *ast.StarExpr:
					idx.aliases[name] = rtTypeBaseName(t)
				case *ast.InterfaceType:
					idx.aliases[name] = name
				default:
					idx.aliases[name] = rtTypeBaseName(ts.Type)
				}
			}
		}
	}
	// Resolve alias chains; fail closed on cycles by capping.
	for name := range idx.aliases {
		seen := map[string]bool{}
		cur := name
		for i := 0; i < 8; i++ {
			if seen[cur] {
				idx.aliases[name] = "" // ambiguous cycle
				break
			}
			seen[cur] = true
			next, ok := idx.aliases[cur]
			if !ok || next == cur {
				idx.aliases[name] = cur
				break
			}
			cur = next
		}
	}
	return idx
}

func (idx *rtIndex) resolve(name string) string {
	if name == "" {
		return ""
	}
	if r, ok := idx.aliases[name]; ok && r != "" {
		return r
	}
	return name
}

func (idx *rtIndex) isContested(expr ast.Expr) bool {
	base := idx.resolve(rtTypeBaseName(expr))
	return rtContestedTypeNames[base]
}

func (idx *rtIndex) findAdapterShape() []rtFinding {
	var out []rtFinding
	adapters := 0
	adapterFile := ""
	for name, f := range idx.files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				holdsHost := false
				for _, field := range st.Fields.List {
					base := idx.resolve(rtTypeBaseName(field.Type))
					if base == "Host" || base == "ReloadHost" {
						holdsHost = true
					}
					if sel, ok := unwrapRTStar(field.Type).(*ast.SelectorExpr); ok && sel.Sel != nil {
						if sel.Sel.Name == "Host" || sel.Sel.Name == "ReloadHost" {
							holdsHost = true
						}
					}
				}
				if !holdsHost {
					continue
				}
				adapters++
				adapterFile = name
				if ts.Name.Name != lipruntimeAdapterTypeName {
					out = append(out, rtFinding{File: name, Owner: ts.Name.Name, Detail: "only " + lipruntimeAdapterTypeName + " may retain concrete Host"})
				}
			}
		}
	}
	if adapters > 1 {
		out = append(out, rtFinding{File: adapterFile, Owner: lipruntimeAdapterTypeName, Detail: fmt.Sprintf("duplicate Host-retaining adapters: %d", adapters)})
	}
	return out
}

func (idx *rtIndex) findPackageGlobals() []rtFinding {
	var out []rtFinding
	for name, f := range idx.files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if vs.Type != nil && (idx.isContested(vs.Type) || rtExprHoldsContested(vs.Type, idx)) {
					out = append(out, rtFinding{File: name, Owner: "var", Detail: "package global retains contested type"})
					continue
				}
				for _, v := range vs.Values {
					if rtExprHoldsContested(v, idx) {
						out = append(out, rtFinding{File: name, Owner: "var", Detail: "package global retains contested type"})
					}
				}
			}
		}
	}
	return out
}

func (idx *rtIndex) findContestedStorage() []rtFinding {
	var out []rtFinding
	for name, st := range idx.structs {
		resolved := idx.resolve(name)
		if name == lipruntimeAdapterTypeName || name == "Runtime" || resolved == "Host" || resolved == "ReloadHost" {
			continue
		}
		if st.Fields == nil {
			continue
		}
		for _, field := range st.Fields.List {
			fname := "embedded"
			if len(field.Names) > 0 && field.Names[0] != nil {
				fname = field.Names[0].Name
			}
			if rtExprHoldsContested(field.Type, idx) {
				out = append(out, rtFinding{File: name, Owner: name + "." + fname, Detail: "contested type storage"})
			}
		}
	}
	return out
}

func (idx *rtIndex) findFactories() []rtFinding {
	var out []rtFinding
	for name, f := range idx.files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Type == nil || fd.Type.Results == nil {
				continue
			}
			recv := rtReceiverTypeName(fd)
			if recv == lipruntimeAdapterTypeName {
				continue
			}
			for _, res := range fd.Type.Results.List {
				if rtExprHoldsContested(res.Type, idx) {
					out = append(out, rtFinding{File: name, Owner: rtFuncOwner(fd), Detail: "factory returns contested type"})
				}
			}
		}
	}
	return out
}

func (idx *rtIndex) findForbiddenSelectors() []rtFinding {
	var out []rtFinding
	for name, f := range idx.files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			contestedLocals := idx.functionContestedLocals(fd)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					if x.Sel == nil {
						return true
					}
					sel := x.Sel.Name
					if rtForbiddenShutdownSelectors[sel] {
						out = append(out, rtFinding{File: name, Owner: rtFuncOwner(fd), Detail: "forbidden ownership selector " + sel})
						return true
					}
					if sel == "ShutdownTracing" {
						out = append(out, rtFinding{File: name, Owner: rtFuncOwner(fd), Detail: "forbidden ownership selector ShutdownTracing"})
						return true
					}
					// Process.Close / direct process close coordination.
					if sel == "Close" {
						if id, ok := x.X.(*ast.Ident); ok {
							if base := contestedLocals[id.Name]; base == "ProcessServices" || base == "ProcessRuntime" || base == "Process" {
								out = append(out, rtFinding{File: name, Owner: rtFuncOwner(fd), Detail: "forbidden ownership selector Process.Close"})
							}
						}
						if id, ok := x.X.(*ast.Ident); ok && (id.Name == "Process" || strings.HasSuffix(id.Name, "Process")) {
							// Only flag when X looks like Process field access via another selector.
						}
						if sx, ok := x.X.(*ast.SelectorExpr); ok && sx.Sel != nil && sx.Sel.Name == "Process" {
							out = append(out, rtFinding{File: name, Owner: rtFuncOwner(fd), Detail: "forbidden ownership selector Process.Close"})
						}
					}
				case *ast.CallExpr:
					// already covered via SelectorExpr walk of Fun
				}
				return true
			})
		}
	}
	return out
}

func (idx *rtIndex) functionContestedLocals(fd *ast.FuncDecl) map[string]string {
	out := map[string]string{}
	addFieldNames := func(fields *ast.FieldList) {
		if fields == nil {
			return
		}
		for _, field := range fields.List {
			base := idx.contestedExprBase(field.Type)
			if base == "" {
				continue
			}
			for _, name := range field.Names {
				if name != nil {
					out[name.Name] = base
				}
			}
		}
	}
	addFieldNames(fd.Type.Params)
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name == "_" || len(x.Rhs) == 0 {
					continue
				}
				rhs := x.Rhs[len(x.Rhs)-1]
				if i < len(x.Rhs) {
					rhs = x.Rhs[i]
				}
				if base := idx.contestedExprBase(rhs); base != "" {
					out[id.Name] = base
				}
			}
		case *ast.DeclStmt:
			gd, ok := x.Decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				return true
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				base := idx.contestedExprBase(vs.Type)
				for i, name := range vs.Names {
					if name == nil || name.Name == "_" {
						continue
					}
					valueBase := base
					if valueBase == "" && i < len(vs.Values) {
						valueBase = idx.contestedExprBase(vs.Values[i])
					}
					if valueBase != "" {
						out[name.Name] = valueBase
					}
				}
			}
		}
		return true
	})
	return out
}

func (idx *rtIndex) contestedExprBase(expr ast.Expr) string {
	switch t := expr.(type) {
	case nil:
		return ""
	case *ast.Ident:
		base := idx.resolve(t.Name)
		if rtContestedTypeNames[base] {
			return base
		}
	case *ast.StarExpr:
		return idx.contestedExprBase(t.X)
	case *ast.SelectorExpr:
		if t.Sel == nil {
			return ""
		}
		switch t.Sel.Name {
		case "Process":
			return "ProcessServices"
		case "Manager", "Coordinator", "ShutdownTracing":
			return t.Sel.Name
		}
		base := idx.resolve(t.Sel.Name)
		if rtContestedTypeNames[base] {
			return base
		}
	case *ast.ArrayType:
		return idx.contestedExprBase(t.Elt)
	case *ast.MapType:
		if base := idx.contestedExprBase(t.Value); base != "" {
			return base
		}
		return idx.contestedExprBase(t.Key)
	case *ast.CompositeLit:
		return idx.contestedExprBase(t.Type)
	case *ast.UnaryExpr:
		return idx.contestedExprBase(t.X)
	case *ast.ParenExpr:
		return idx.contestedExprBase(t.X)
	}
	return ""
}

func rtExprHoldsContested(expr ast.Expr, idx *rtIndex) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return rtContestedTypeNames[idx.resolve(t.Name)]
	case *ast.StarExpr:
		return rtExprHoldsContested(t.X, idx)
	case *ast.SelectorExpr:
		return rtContestedTypeNames[t.Sel.Name] || rtContestedTypeNames[idx.resolve(t.Sel.Name)]
	case *ast.ArrayType:
		return rtExprHoldsContested(t.Elt, idx)
	case *ast.Ellipsis:
		return rtExprHoldsContested(t.Elt, idx)
	case *ast.MapType:
		return rtExprHoldsContested(t.Key, idx) || rtExprHoldsContested(t.Value, idx)
	case *ast.ChanType:
		return rtExprHoldsContested(t.Value, idx)
	case *ast.FuncType:
		if t.Params != nil {
			for _, f := range t.Params.List {
				if rtExprHoldsContested(f.Type, idx) {
					return true
				}
			}
		}
		if t.Results != nil {
			for _, f := range t.Results.List {
				if rtExprHoldsContested(f.Type, idx) {
					return true
				}
			}
		}
	case *ast.IndexExpr:
		return rtExprHoldsContested(t.X, idx) || rtExprHoldsContested(t.Index, idx)
	case *ast.IndexListExpr:
		if rtExprHoldsContested(t.X, idx) {
			return true
		}
		for _, i := range t.Indices {
			if rtExprHoldsContested(i, idx) {
				return true
			}
		}
	case *ast.CompositeLit:
		return rtExprHoldsContested(t.Type, idx)
	case *ast.UnaryExpr:
		return rtExprHoldsContested(t.X, idx)
	case *ast.ParenExpr:
		return rtExprHoldsContested(t.X, idx)
	}
	return false
}

func unwrapRTStar(expr ast.Expr) ast.Expr {
	for {
		st, ok := expr.(*ast.StarExpr)
		if !ok {
			return expr
		}
		expr = st.X
	}
}

func rtIsInterfaceType(expr ast.Expr, files map[string]*ast.File) bool {
	switch t := expr.(type) {
	case *ast.InterfaceType:
		return true
	case *ast.Ident:
		matches := 0
		isIface := false
		for _, f := range files {
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name == nil || ts.Name.Name != t.Name {
						continue
					}
					matches++
					if _, ok := ts.Type.(*ast.InterfaceType); ok {
						isIface = true
					}
				}
			}
		}
		if matches > 1 {
			return false // fail closed on ambiguous declarations
		}
		return isIface
	case *ast.SelectorExpr, *ast.StarExpr:
		return false
	}
	return false
}

func rtNestedOwnership(expr ast.Expr, files map[string]*ast.File, visiting map[string]bool) string {
	forbidden := rtForbiddenFields()
	name := rtTypeBaseName(expr)
	if name == "" || visiting[name] {
		return ""
	}
	visiting[name] = true
	decls := 0
	var found string
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil || ts.Name.Name != name {
					continue
				}
				decls++
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				for _, field := range st.Fields.List {
					fname := "embedded"
					if len(field.Names) > 0 && field.Names[0] != nil {
						fname = field.Names[0].Name
					} else {
						fname = rtTypeBaseName(field.Type)
					}
					if forbidden[fname] || rtForbiddenShutdownSelectors[fname] {
						found = fname
					}
					if nested := rtNestedOwnership(field.Type, files, visiting); nested != "" {
						found = nested
					}
				}
			}
		}
	}
	if decls > 1 {
		return "ambiguous:" + name
	}
	return found
}

func rtTypeBaseName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return rtTypeBaseName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	default:
		return ""
	}
}

func rtReceiverTypeName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	return rtTypeBaseName(fd.Recv.List[0].Type)
}

func rtFuncOwner(fd *ast.FuncDecl) string {
	name := fd.Name.Name
	if recv := rtReceiverTypeName(fd); recv != "" {
		return recv + "." + name
	}
	return name
}

func joinRTFindings(fs []rtFinding) string {
	parts := make([]string, 0, len(fs))
	for _, f := range fs {
		parts = append(parts, f.String())
	}
	return strings.Join(parts, "\n")
}
