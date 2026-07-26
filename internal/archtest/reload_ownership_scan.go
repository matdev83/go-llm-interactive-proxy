package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
)

// reloadOwnershipScanResult collects architecture violations for runtime-reload
// ownership gates (task 1.1).
type reloadOwnershipScanResult struct {
	TracerBootstraps     []string
	MetricsConstructions []string
	ProcessWorkers       []string
	MutationSetters      []string
	WatcherMechanisms    []string
}

func parseGoSource(filename string, src string) (*token.FileSet, *ast.File, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, err
	}
	return fset, f, nil
}

func formatPos(fset *token.FileSet, pos token.Pos) string {
	p := fset.Position(pos)
	name := filepath.ToSlash(p.Filename)
	// Prefer path-qualified names when available so live-tree gates can
	// distinguish same-basename files across packages. Fixture filenames that
	// are already basenames remain unchanged.
	if name == "" {
		name = "unknown"
	}
	return fmt.Sprintf("%s:%d:%d", name, p.Line, p.Column)
}

// importAliasToPath maps local selector identifiers to import paths.
func importAliasToPath(f *ast.File) map[string]string {
	out := make(map[string]string)
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				continue
			}
			out[imp.Name.Name] = path
			continue
		}
		name := path
		if i := strings.LastIndex(path, "/"); i >= 0 {
			name = path[i+1:]
		}
		out[name] = path
	}
	return out
}

func fileImportPaths(f *ast.File) []string {
	var out []string
	for _, imp := range f.Imports {
		out = append(out, strings.Trim(imp.Path.Value, `"`))
	}
	return out
}

func callSelector(call *ast.CallExpr) (recv string, name string, ok bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return "", "", false
	}
	switch x := sel.X.(type) {
	case *ast.Ident:
		return x.Name, sel.Sel.Name, true
	default:
		return "", sel.Sel.Name, true
	}
}

func resolveImportPath(aliasToPath map[string]string, alias string) string {
	if alias == "" {
		return ""
	}
	return aliasToPath[alias]
}

func pathHasSuffix(path, suffix string) bool {
	return path == suffix || strings.HasSuffix(path, "/"+suffix) || strings.HasSuffix(path, suffix)
}

// funcValueAliases tracks identifiers bound to package.Selector function values.
type funcValueAliases map[string]string // localIdent -> "path.Name" or "kind:path.Name"

func collectFuncValueAliases(f *ast.File, aliasToPath map[string]string) funcValueAliases {
	out := funcValueAliases{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) != len(n.Rhs) {
				return true
			}
			for i, lhs := range n.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name == "_" {
					continue
				}
				if label := selectorFuncLabel(n.Rhs[i], aliasToPath); label != "" {
					out[id.Name] = label
				} else if rhs, ok := n.Rhs[i].(*ast.Ident); ok {
					if prev, ok := out[rhs.Name]; ok {
						out[id.Name] = prev
					}
				}
			}
		case *ast.ValueSpec:
			for i, name := range n.Names {
				if name == nil || name.Name == "_" || i >= len(n.Values) {
					continue
				}
				if label := selectorFuncLabel(n.Values[i], aliasToPath); label != "" {
					out[name.Name] = label
				} else if rhs, ok := n.Values[i].(*ast.Ident); ok {
					if prev, ok := out[rhs.Name]; ok {
						out[name.Name] = prev
					}
				}
			}
		}
		return true
	})
	return out
}

// liveFuncAliases is a lexical, statement-ordered function-value alias environment.
// Assignments replace/kill prior bindings; := / blocks introduce nested scopes.
// Branch merge preserves a forbidden (os probe) target if any reachable branch retains it.
type liveFuncAliases struct {
	parent *liveFuncAliases
	binds  map[string]string // name -> canonical label; "" means non-probe / killed
}

func newLiveFuncAliases(parent *liveFuncAliases) *liveFuncAliases {
	return &liveFuncAliases{parent: parent, binds: map[string]string{}}
}

func (a *liveFuncAliases) lookup(name string) (string, bool) {
	for cur := a; cur != nil; cur = cur.parent {
		if v, ok := cur.binds[name]; ok {
			return v, true
		}
	}
	return "", false
}

func (a *liveFuncAliases) declare(name, label string) {
	if a == nil || name == "" || name == "_" {
		return
	}
	a.binds[name] = label
}

func (a *liveFuncAliases) assign(name, label string) {
	if a == nil || name == "" || name == "_" {
		return
	}
	for cur := a; cur != nil; cur = cur.parent {
		if _, ok := cur.binds[name]; ok {
			cur.binds[name] = label
			return
		}
	}
	a.binds[name] = label
}

func (a *liveFuncAliases) flat() funcValueAliases {
	out := funcValueAliases{}
	seen := map[string]bool{}
	for cur := a; cur != nil; cur = cur.parent {
		names := make([]string, 0, len(cur.binds))
		for n := range cur.binds {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if seen[n] {
				continue
			}
			seen[n] = true
			if v := cur.binds[n]; v != "" {
				out[n] = v
			}
		}
	}
	return out
}

func (a *liveFuncAliases) cloneTree() *liveFuncAliases {
	if a == nil {
		return newLiveFuncAliases(nil)
	}
	var cloneNode func(*liveFuncAliases) *liveFuncAliases
	cloneNode = func(n *liveFuncAliases) *liveFuncAliases {
		if n == nil {
			return nil
		}
		parent := cloneNode(n.parent)
		out := newLiveFuncAliases(parent)
		names := make([]string, 0, len(n.binds))
		for name := range n.binds {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			out.binds[name] = n.binds[name]
		}
		return out
	}
	return cloneNode(a)
}

func forkLiveFuncAliases(a *liveFuncAliases) (*liveFuncAliases, map[string]struct{}) {
	flat := a.flat()
	env := newLiveFuncAliases(nil)
	pre := map[string]struct{}{}
	names := make([]string, 0, len(flat)+8)
	// Include empty bindings (kills) from the nearest scopes.
	seen := map[string]bool{}
	for cur := a; cur != nil; cur = cur.parent {
		for name := range cur.binds {
			if seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		pre[name] = struct{}{}
		v, _ := a.lookup(name)
		env.binds[name] = v
	}
	return env, pre
}

func mergeLiveFuncAliases(target *liveFuncAliases, preexisting map[string]struct{}, branches ...*liveFuncAliases) {
	if target == nil || len(preexisting) == 0 || len(branches) == 0 {
		return
	}
	names := make([]string, 0, len(preexisting))
	for name := range preexisting {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		anyProbe := false
		anySet := false
		var agreed string
		allAgree := true
		for _, b := range branches {
			if b == nil {
				continue
			}
			v, ok := b.lookup(name)
			if !ok {
				allAgree = false
				continue
			}
			anySet = true
			if isOSProbeFuncLabel(v) {
				anyProbe = true
			}
			if agreed == "" && allAgree {
				agreed = v
			} else if v != agreed {
				allAgree = false
			}
		}
		if anyProbe {
			// Conservative for detection: retain forbidden target if any branch may.
			target.assign(name, osStatLabelFromBranches(branches, name))
		} else if anySet && allAgree {
			target.assign(name, agreed)
		} else if anySet {
			target.assign(name, "")
		}
	}
}

func osStatLabelFromBranches(branches []*liveFuncAliases, name string) string {
	for _, b := range branches {
		if b == nil {
			continue
		}
		if v, ok := b.lookup(name); ok && isOSProbeFuncLabel(v) {
			return v
		}
	}
	return "os.Stat"
}

func isOSProbeFuncLabel(label string) bool {
	if label == "" {
		return false
	}
	for _, base := range []string{"Stat", "Lstat", "ReadDir", "ReadFile", "OpenFile", "Open"} {
		suf := "os." + base
		if label == suf || strings.HasSuffix(label, "/"+suf) {
			return true
		}
	}
	return false
}

func resolveFuncAliasRHS(expr ast.Expr, live *liveFuncAliases, aliasToPath map[string]string, pkgPath string) string {
	expr = unwrapParenExpr(expr)
	if label := selectorFuncLabel(expr, aliasToPath); label != "" {
		return label
	}
	switch e := expr.(type) {
	case *ast.Ident:
		if live != nil {
			if v, ok := live.lookup(e.Name); ok {
				return v
			}
		}
		// Package-local free function used as a value (e.g. cacheStat).
		return freeFuncIdentity(pkgPath, e.Name)
	}
	return ""
}

func applyLiveFuncAliasAssign(assign *ast.AssignStmt, live *liveFuncAliases, aliasToPath map[string]string, pkgPath string) {
	if assign == nil || live == nil || len(assign.Lhs) != len(assign.Rhs) {
		return
	}
	define := assign.Tok == token.DEFINE
	for i, lhs := range assign.Lhs {
		id, ok := lhs.(*ast.Ident)
		if !ok || id.Name == "_" {
			continue
		}
		label := resolveFuncAliasRHS(assign.Rhs[i], live, aliasToPath, pkgPath)
		if define {
			live.declare(id.Name, label)
		} else {
			live.assign(id.Name, label)
		}
	}
}

func applyLiveFuncAliasValueSpec(spec *ast.ValueSpec, live *liveFuncAliases, aliasToPath map[string]string, pkgPath string) {
	if spec == nil || live == nil {
		return
	}
	for i, name := range spec.Names {
		if name == nil || name.Name == "_" {
			continue
		}
		label := ""
		if i < len(spec.Values) {
			label = resolveFuncAliasRHS(spec.Values[i], live, aliasToPath, pkgPath)
		}
		live.declare(name.Name, label)
	}
}

func unwrapParenExpr(expr ast.Expr) ast.Expr {
	for {
		p, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = p.X
	}
}

func selectorFuncLabel(expr ast.Expr, aliasToPath map[string]string) string {
	expr = unwrapParenExpr(expr)
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return ""
	}
	recv, ok := unwrapParenExpr(sel.X).(*ast.Ident)
	if !ok {
		return ""
	}
	path := resolveImportPath(aliasToPath, recv.Name)
	if path == "" {
		return ""
	}
	return path + "." + sel.Sel.Name
}

func callViaFuncAlias(call *ast.CallExpr, aliases funcValueAliases) (label string, ok bool) {
	id, ok := call.Fun.(*ast.Ident)
	if !ok {
		return "", false
	}
	label, ok = aliases[id.Name]
	return label, ok
}

// findTracerBootstraps reports otel.SetTracerProvider and tracing.Init via import path
// and function-value aliases.
func findTracerBootstraps(fset *token.FileSet, f *ast.File) []string {
	aliasToPath := importAliasToPath(f)
	aliases := collectFuncValueAliases(f, aliasToPath)
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if label, ok := callViaFuncAlias(call, aliases); ok {
			switch {
			case strings.HasSuffix(label, "/otel.SetTracerProvider") || label == "go.opentelemetry.io/otel.SetTracerProvider":
				out = append(out, formatPos(fset, call.Pos())+": otel.SetTracerProvider")
			case strings.HasSuffix(label, "internal/infra/tracing.Init"):
				out = append(out, formatPos(fset, call.Pos())+": tracing.Init")
			}
			return true
		}
		recv, name, ok := callSelector(call)
		if !ok {
			return true
		}
		path := resolveImportPath(aliasToPath, recv)
		switch {
		case name == "SetTracerProvider" && (path == "go.opentelemetry.io/otel" || strings.HasSuffix(path, "/otel")):
			out = append(out, formatPos(fset, call.Pos())+": otel.SetTracerProvider")
		case name == "Init" && pathHasSuffix(path, "internal/infra/tracing"):
			out = append(out, formatPos(fset, call.Pos())+": tracing.Init")
		}
		return true
	})
	return out
}

// findMetricsConstructions reports dedicated Prometheus registry / metrics bundle
// construction via import path and function-value aliases.
func findMetricsConstructions(fset *token.FileSet, f *ast.File) []string {
	aliasToPath := importAliasToPath(f)
	aliases := collectFuncValueAliases(f, aliasToPath)
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if label, ok := callViaFuncAlias(call, aliases); ok {
			switch {
			case strings.HasSuffix(label, "internal/infra/metrics.NewBundle"):
				out = append(out, formatPos(fset, call.Pos())+": metrics.NewBundle")
			case strings.HasSuffix(label, "internal/infra/metrics.NewRegistry"):
				out = append(out, formatPos(fset, call.Pos())+": metrics.NewRegistry")
			case strings.HasSuffix(label, "/prometheus.NewRegistry") || label == "github.com/prometheus/client_golang/prometheus.NewRegistry":
				out = append(out, formatPos(fset, call.Pos())+": prometheus.NewRegistry")
			case strings.Contains(label, "prometheus/collectors.NewGoCollector"):
				out = append(out, formatPos(fset, call.Pos())+": collectors.NewGoCollector")
			case strings.Contains(label, "prometheus/collectors.NewProcessCollector"):
				out = append(out, formatPos(fset, call.Pos())+": collectors.NewProcessCollector")
			}
			return true
		}
		recv, name, ok := callSelector(call)
		if !ok {
			return true
		}
		path := resolveImportPath(aliasToPath, recv)
		switch {
		case pathHasSuffix(path, "internal/infra/metrics") && (name == "NewBundle" || name == "NewRegistry"):
			out = append(out, formatPos(fset, call.Pos())+": metrics."+name)
		case (path == "github.com/prometheus/client_golang/prometheus" || strings.HasSuffix(path, "/prometheus")) &&
			name == "NewRegistry":
			out = append(out, formatPos(fset, call.Pos())+": prometheus.NewRegistry")
		case (strings.Contains(path, "prometheus/collectors") || strings.HasSuffix(path, "/collectors")) &&
			(name == "NewGoCollector" || name == "NewProcessCollector"):
			out = append(out, formatPos(fset, call.Pos())+": collectors."+name)
		}
		return true
	})
	return out
}

// findProcessWorkerConstructions reports terminal-work processor construction via
// import path and function-value aliases.
func findProcessWorkerConstructions(fset *token.FileSet, f *ast.File) []string {
	aliasToPath := importAliasToPath(f)
	aliases := collectFuncValueAliases(f, aliasToPath)
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if label, ok := callViaFuncAlias(call, aliases); ok {
			if strings.HasSuffix(label, "internal/core/terminalwork/app.NewProcessor") {
				out = append(out, formatPos(fset, call.Pos())+": terminalworkapp.NewProcessor")
			}
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "NewProcessor" {
			out = append(out, formatPos(fset, call.Pos())+": NewProcessor")
			return true
		}
		recv, name, ok := callSelector(call)
		if !ok || name != "NewProcessor" {
			return true
		}
		path := resolveImportPath(aliasToPath, recv)
		if pathHasSuffix(path, "internal/core/terminalwork/app") {
			out = append(out, formatPos(fset, call.Pos())+": terminalworkapp.NewProcessor")
		}
		return true
	})
	return out
}

const lipModulePath = "github.com/matdev83/go-llm-interactive-proxy"

func packagePathFromFilename(filename string) string {
	p := filepath.ToSlash(filename)
	for _, root := range []string{"internal/", "cmd/", "pkg/"} {
		if i := strings.Index(p, root); i >= 0 {
			dir := filepath.ToSlash(filepath.Dir(p[i:]))
			if dir == "." || dir == "" {
				return ""
			}
			return lipModulePath + "/" + dir
		}
	}
	return ""
}

func isExcludedCanonical(canon string) bool {
	if canon == "" {
		return false
	}
	return canon == "snapshotgen.RuntimeGeneration" ||
		strings.HasSuffix(canon, "/internal/core/snapshotgen.RuntimeGeneration")
}

func isApprovedActiveCanonical(canon string) bool {
	if canon == "" || isExcludedCanonical(canon) {
		return false
	}
	switch canon {
	case lipModulePath + "/internal/core/runtime.Executor",
		lipModulePath + "/internal/core/runtime.App",
		lipModulePath + "/internal/infra/runtimebundle.Built",
		lipModulePath + "/internal/infra/runtimehost.RuntimeGeneration",
		lipModulePath + "/internal/infra/runtimehost.Runtime",
		lipModulePath + "/pkg/lipruntime.Runtime",
		// Synthetic fixture short names standing in for approved identities.
		"Executor", "RuntimeGeneration", "Runtime", "App", "Built":
		return true
	default:
		return false
	}
}

func localFixtureActiveName(name string) bool {
	switch name {
	case "Executor", "RuntimeGeneration", "Runtime", "App", "Built":
		return true
	default:
		return false
	}
}

func typeNameHint(expr ast.Expr) string {
	expr = unwrapParenExpr(expr)
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return typeNameHint(e.X)
	case *ast.SelectorExpr:
		if e.Sel == nil {
			return ""
		}
		if id, ok := unwrapParenExpr(e.X).(*ast.Ident); ok {
			return id.Name + "." + e.Sel.Name
		}
		return e.Sel.Name
	case *ast.IndexExpr:
		return typeNameHint(e.X)
	}
	return ""
}

func isMutatingMethodName(name string) bool {
	if name == "" {
		return false
	}
	if name == "Set" {
		return true
	}
	prefixes := []string{"Set", "Update", "Replace", "Swap", "Reset", "Configure", "Rewire", "Mutate"}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// exportTypeIndex is a cross-package declaration index of free functions,
// methods, type aliases, and struct fields keyed by canonical import path.
type exportTypeIndex struct {
	funcs   map[string]map[string]string            // pkgPath -> func -> canonical return
	methods map[string]map[string]string            // pkgPath -> "Type.Method" -> canonical return
	types   map[string]map[string]string            // pkgPath -> localName -> canonical
	fields  map[string]map[string]map[string]string // pkgPath -> type -> field -> canonical
}

func newExportTypeIndex() *exportTypeIndex {
	return &exportTypeIndex{
		funcs:   map[string]map[string]string{},
		methods: map[string]map[string]string{},
		types:   map[string]map[string]string{},
		fields:  map[string]map[string]map[string]string{},
	}
}

// pkgTypeIndex is a package-aware declaration index spanning all files in one
// package directory, optionally backed by a cross-package export index.
type pkgTypeIndex struct {
	pkgPath       string
	aliases       map[string]string            // local type name -> canonical
	funcReturns   map[string]string            // free function name -> canonical return
	typeMethods   map[string]map[string]string // typeCanon ({path}.{Type}) -> method -> return
	structFields  map[string]map[string]string // local type name -> field -> canonical
	exports       *exportTypeIndex
	embedEdges    []ifaceEmbedEdge
	definedCopies [][2]string // [definedLocalName, underlyingCanonical]
}

type ifaceEmbedEdge struct {
	typeName string // local declaring type name (destination)
	embed    string // canonical embedded type ({path}.{Type} or local)
}

type parsedOverlayFile struct {
	filename    string
	fset        *token.FileSet
	file        *ast.File
	pkgDir      string
	pkgPath     string
	aliasToPath map[string]string
}

func resolveCanonicalType(expr ast.Expr, pkgPath string, fileAliases map[string]string, localAliases map[string]string) string {
	expr = unwrapParenExpr(expr)
	switch e := expr.(type) {
	case *ast.StarExpr:
		return resolveCanonicalType(e.X, pkgPath, fileAliases, localAliases)
	case *ast.Ident:
		if localAliases != nil {
			if c, ok := localAliases[e.Name]; ok {
				return c
			}
		}
		if pkgPath != "" {
			full := pkgPath + "." + e.Name
			if isApprovedActiveCanonical(full) || localFixtureActiveName(e.Name) {
				return full
			}
			return full
		}
		if localFixtureActiveName(e.Name) {
			return e.Name
		}
		return e.Name
	case *ast.SelectorExpr:
		if e.Sel == nil {
			return ""
		}
		pkgIdent, ok := unwrapParenExpr(e.X).(*ast.Ident)
		if !ok {
			return e.Sel.Name
		}
		if path := fileAliases[pkgIdent.Name]; path != "" {
			return path + "." + e.Sel.Name
		}
		return pkgIdent.Name + "." + e.Sel.Name
	case *ast.IndexExpr:
		return resolveCanonicalType(e.X, pkgPath, fileAliases, localAliases)
	}
	return ""
}

func firstResultTypeExpr(fn *ast.FuncDecl) ast.Expr {
	if fn.Type == nil || fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
		return nil
	}
	return fn.Type.Results.List[0].Type
}

func recvTypeLocalName(expr ast.Expr) string {
	expr = unwrapParenExpr(expr)
	switch e := expr.(type) {
	case *ast.StarExpr:
		return recvTypeLocalName(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return recvTypeLocalName(e.X)
	case *ast.SelectorExpr:
		if e.Sel != nil {
			return e.Sel.Name
		}
	}
	return ""
}

func buildPkgTypeIndex(files []*parsedOverlayFile, exports *exportTypeIndex) *pkgTypeIndex {
	idx := &pkgTypeIndex{
		aliases:      map[string]string{},
		funcReturns:  map[string]string{},
		typeMethods:  map[string]map[string]string{},
		structFields: map[string]map[string]string{},
		exports:      exports,
	}
	if len(files) == 0 {
		return idx
	}
	idx.pkgPath = files[0].pkgPath

	// Pass 1: type aliases and struct field shells.
	for _, pf := range files {
		for _, decl := range pf.file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil || ts.Type == nil {
					continue
				}
				if ts.Assign.IsValid() {
					canon := resolveCanonicalType(ts.Type, idx.pkgPath, pf.aliasToPath, nil)
					idx.aliases[ts.Name.Name] = canon
					continue
				}
				if idx.pkgPath != "" {
					idx.aliases[ts.Name.Name] = idx.pkgPath + "." + ts.Name.Name
				} else if localFixtureActiveName(ts.Name.Name) {
					idx.aliases[ts.Name.Name] = ts.Name.Name
				}
			}
		}
	}

	resolveLocal := func(expr ast.Expr, fileAliases map[string]string) string {
		canon := resolveCanonicalType(expr, idx.pkgPath, fileAliases, idx.aliases)
		if base, ok := idx.aliases[canon]; ok {
			return base
		}
		// Resolve short local alias chains: ActiveExec -> Executor -> canonical
		if !strings.Contains(canon, "/") && !strings.Contains(canon, ".") {
			if base, ok := idx.aliases[canon]; ok {
				return base
			}
		}
		return canon
	}

	// Pass 2: struct fields, interface methods, and function/method returns.
	// Embedded interface edges are recorded only; method-set propagation runs
	// later in a deterministic cross-package fixpoint.
	for _, pf := range files {
		for _, decl := range pf.file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				retExpr := firstResultTypeExpr(d)
				if retExpr == nil || d.Name == nil {
					continue
				}
				ret := resolveLocal(retExpr, pf.aliasToPath)
				if d.Recv != nil && len(d.Recv.List) > 0 {
					recv := recvTypeLocalName(d.Recv.List[0].Type)
					if recv != "" {
						idx.setTypeMethod(idx.localTypeCanon(recv), d.Name.Name, ret)
					}
				} else {
					idx.funcReturns[d.Name.Name] = ret
				}
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name == nil || ts.Type == nil {
						continue
					}
					switch typ := ts.Type.(type) {
					case *ast.StructType:
						if typ.Fields == nil {
							continue
						}
						fields := map[string]string{}
						for _, field := range typ.Fields.List {
							tn := resolveLocal(field.Type, pf.aliasToPath)
							for _, name := range field.Names {
								if name != nil {
									fields[name.Name] = tn
								}
							}
						}
						idx.structFields[ts.Name.Name] = fields
					case *ast.InterfaceType:
						for _, emb := range indexInterfaceMethods(idx, ts.Name.Name, typ, pf.aliasToPath, resolveLocal) {
							idx.embedEdges = append(idx.embedEdges, ifaceEmbedEdge{typeName: ts.Name.Name, embed: emb})
						}
					default:
						if ts.Assign.IsValid() {
							continue
						}
						// Defined named type over another named type (e.g. type HostI Host).
						switch typ.(type) {
						case *ast.Ident, *ast.SelectorExpr:
							under := resolveLocal(typ, pf.aliasToPath)
							if under != "" {
								idx.definedCopies = append(idx.definedCopies, [2]string{ts.Name.Name, under})
							}
						}
					}
				}
			}
		}
	}
	return idx
}

// propagatePkgEmbeds copies method sets through local and cross-package embedded
// interfaces / defined type copies once. Returns true when the index grew.
// Cross-package embeds resolve only via the canonical source package/type;
// unresolved sources that collide with a local same-short-name type fail loudly.
func propagatePkgEmbeds(idx *pkgTypeIndex) (bool, error) {
	if idx == nil {
		return false, nil
	}
	changed := false
	for _, e := range idx.embedEdges {
		grew, err := copyMethodSet(idx, e.typeName, e.embed)
		if err != nil {
			return changed, err
		}
		if grew {
			changed = true
		}
	}
	for _, c := range idx.definedCopies {
		grew, err := copyMethodSet(idx, c[0], c[1])
		if err != nil {
			return changed, err
		}
		if grew {
			changed = true
		}
	}
	return changed, nil
}

func embedFixpointBound(indexes []*pkgTypeIndex) int {
	total := 0
	for _, idx := range indexes {
		if idx == nil {
			continue
		}
		methodCount := 0
		for _, methods := range idx.typeMethods {
			methodCount += len(methods)
		}
		total += len(idx.funcReturns) + methodCount + len(idx.embedEdges) + len(idx.definedCopies)
	}
	if total < 1 {
		total = 1
	}
	// Each iteration adds at least one method entry when progress is made;
	// allow a pass per package over the total edge/method budget.
	return (total + 1) * (len(indexes) + 1)
}

// indexInterfaceMethods records named interface method return types and returns
// embedded interface canonical type names for later method-set propagation.
func indexInterfaceMethods(
	idx *pkgTypeIndex,
	typeName string,
	iface *ast.InterfaceType,
	fileAliases map[string]string,
	resolveLocal func(ast.Expr, map[string]string) string,
) []string {
	var embeds []string
	if iface == nil || iface.Methods == nil {
		return embeds
	}
	typeCanon := idx.localTypeCanon(typeName)
	for _, field := range iface.Methods.List {
		if len(field.Names) == 0 {
			emb := resolveLocal(field.Type, fileAliases)
			if emb != "" {
				embeds = append(embeds, emb)
			}
			continue
		}
		ft, ok := field.Type.(*ast.FuncType)
		if !ok {
			continue
		}
		var ret string
		if ft.Results != nil && len(ft.Results.List) > 0 {
			ret = resolveLocal(ft.Results.List[0].Type, fileAliases)
		}
		for _, name := range field.Names {
			if name == nil {
				continue
			}
			idx.setTypeMethod(typeCanon, name.Name, ret)
		}
	}
	return embeds
}

// copyMethodSet copies method return entries from srcCanon into dstLocal type
// name. Identities are canonical ({importPath}.{Type}): a cross-package embed
// consults only the source package/type method set and never a destination-local
// same-short-name type. Returns true when at least one new entry was added.
func copyMethodSet(idx *pkgTypeIndex, dstLocal, srcCanon string) (bool, error) {
	if idx == nil || dstLocal == "" || srcCanon == "" {
		return false, nil
	}
	dstCanon := idx.localTypeCanon(dstLocal)
	srcMethods, err := idx.lookupTypeMethods(srcCanon)
	if err != nil {
		return false, err
	}
	if len(srcMethods) == 0 {
		return false, nil
	}
	changed := false
	for method, ret := range srcMethods {
		if idx.setTypeMethod(dstCanon, method, ret) {
			changed = true
		}
	}
	return changed, nil
}

func populateExportIndex(exports *exportTypeIndex, pkgPath string, idx *pkgTypeIndex) {
	if exports == nil || pkgPath == "" {
		return
	}
	if exports.funcs[pkgPath] == nil {
		exports.funcs[pkgPath] = map[string]string{}
	}
	if exports.methods[pkgPath] == nil {
		exports.methods[pkgPath] = map[string]string{}
	}
	if exports.types[pkgPath] == nil {
		exports.types[pkgPath] = map[string]string{}
	}
	if exports.fields[pkgPath] == nil {
		exports.fields[pkgPath] = map[string]map[string]string{}
	}
	maps.Copy(exports.types[pkgPath], idx.aliases)
	maps.Copy(exports.funcs[pkgPath], idx.funcReturns)
	for typeCanon, methods := range idx.typeMethods {
		short := shortTypeName(typeCanon)
		if short == "" {
			continue
		}
		// Only publish methods owned by this package's canonical types.
		if pkg := packageOfCanonical(typeCanon); pkg != "" && pkg != pkgPath {
			continue
		}
		for method, ret := range methods {
			exports.methods[pkgPath][short+"."+method] = ret
		}
	}
	for typ, fields := range idx.structFields {
		cp := map[string]string{}
		maps.Copy(cp, fields)
		exports.fields[pkgPath][typ] = cp
	}
}

func (idx *pkgTypeIndex) localTypeCanon(local string) string {
	if idx == nil || local == "" {
		return local
	}
	if c, ok := idx.aliases[local]; ok && c != "" {
		return c
	}
	if idx.pkgPath != "" {
		return idx.pkgPath + "." + local
	}
	return local
}

func (idx *pkgTypeIndex) setTypeMethod(typeCanon, method, ret string) bool {
	if idx == nil || typeCanon == "" || method == "" {
		return false
	}
	if idx.typeMethods[typeCanon] == nil {
		idx.typeMethods[typeCanon] = map[string]string{}
	}
	if _, ok := idx.typeMethods[typeCanon][method]; ok {
		return false
	}
	idx.typeMethods[typeCanon][method] = ret
	return true
}

// lookupTypeMethods returns the method set for an exact canonical type identity.
// Cross-package lookups consult only the source package export map for that type
// short name within its package; they never fall back to a local collision.
func (idx *pkgTypeIndex) lookupTypeMethods(srcCanon string) (map[string]string, error) {
	if idx == nil || srcCanon == "" {
		return nil, nil
	}
	srcPkg := packageOfCanonical(srcCanon)
	srcShort := shortTypeName(srcCanon)
	if srcShort == "" {
		return nil, nil
	}

	crossPackage := srcPkg != "" && srcPkg != idx.pkgPath

	// Local / same-package embed: resolve only within this package's identity.
	if !crossPackage {
		out := map[string]string{}
		// Prefer exact canonical key.
		if methods := idx.typeMethods[srcCanon]; methods != nil {
			maps.Copy(out, methods)
			return out, nil
		}
		// Fixture short name or alias-equivalent local identity.
		localCanon := idx.localTypeCanon(srcShort)
		if methods := idx.typeMethods[localCanon]; methods != nil {
			maps.Copy(out, methods)
			return out, nil
		}
		if methods := idx.typeMethods[srcShort]; methods != nil {
			maps.Copy(out, methods)
		}
		return out, nil
	}

	// Cross-package: only the canonical source package/type — never local short name.
	out := map[string]string{}
	if idx.exports == nil {
		return out, fmt.Errorf("unresolved embedded interface %s: no export index", srcCanon)
	}
	methods := idx.exports.methods[srcPkg]
	types := idx.exports.types[srcPkg]
	if methods == nil && types == nil {
		// Source package absent from the overlay. Never bind a local same-short-name
		// type; fail loudly for in-module sources or when a local collision exists.
		if strings.HasPrefix(srcPkg, lipModulePath) || idx.hasLocalTypeName(srcShort) {
			return out, fmt.Errorf("unresolved embedded interface %s: source package %s not in overlay", srcCanon, srcPkg)
		}
		return out, nil
	}
	if types != nil {
		if _, ok := types[srcShort]; !ok {
			return out, fmt.Errorf("unresolved embedded interface %s: type %s not declared in %s", srcCanon, srcShort, srcPkg)
		}
	}
	prefix := srcShort + "."
	for key, ret := range methods {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		method := key[len(prefix):]
		if method == "" || strings.Contains(method, ".") {
			continue
		}
		out[method] = ret
	}
	return out, nil
}

func (idx *pkgTypeIndex) hasLocalTypeName(short string) bool {
	if idx == nil || short == "" {
		return false
	}
	if _, ok := idx.aliases[short]; ok {
		return true
	}
	localCanon := idx.localTypeCanon(short)
	if _, ok := idx.typeMethods[localCanon]; ok {
		return true
	}
	if _, ok := idx.typeMethods[short]; ok {
		return true
	}
	if _, ok := idx.structFields[short]; ok {
		return true
	}
	return false
}

func shortTypeName(canon string) string {
	if canon == "" {
		return ""
	}
	if i := strings.LastIndex(canon, "."); i >= 0 {
		return canon[i+1:]
	}
	return canon
}

func packageOfCanonical(canon string) string {
	if i := strings.LastIndex(canon, "."); i >= 0 {
		return canon[:i]
	}
	return ""
}

func (idx *pkgTypeIndex) shortTypeName(canon string) string {
	return shortTypeName(canon)
}

func (idx *pkgTypeIndex) packageOfCanonical(canon string) string {
	if pkg := packageOfCanonical(canon); pkg != "" {
		return pkg
	}
	if idx != nil {
		return idx.pkgPath
	}
	return ""
}

func (idx *pkgTypeIndex) methodReturn(typeCanon, method string) string {
	if idx == nil || typeCanon == "" || method == "" {
		return ""
	}
	if methods := idx.typeMethods[typeCanon]; methods != nil {
		if t, ok := methods[method]; ok {
			return t
		}
	}
	// Exact canonical miss: try local alias form for same-package receivers.
	short := shortTypeName(typeCanon)
	localCanon := idx.localTypeCanon(short)
	if localCanon != typeCanon {
		if methods := idx.typeMethods[localCanon]; methods != nil {
			if t, ok := methods[method]; ok {
				return t
			}
		}
	}
	pkg := packageOfCanonical(typeCanon)
	if pkg == "" {
		pkg = idx.pkgPath
	}
	if idx.exports != nil && pkg != "" {
		if t := idx.exports.methods[pkg][short+"."+method]; t != "" {
			return t
		}
	}
	return ""
}

func (idx *pkgTypeIndex) callReturnCanonical(call *ast.CallExpr, scope *mutScope, fileAliases map[string]string) string {
	switch fun := unwrapParenExpr(call.Fun).(type) {
	case *ast.Ident:
		if t, ok := idx.funcReturns[fun.Name]; ok {
			return t
		}
	case *ast.SelectorExpr:
		if fun.Sel == nil {
			return ""
		}
		// Package-qualified free function: runtime.GetExecutor()
		if pkgIdent, ok := unwrapParenExpr(fun.X).(*ast.Ident); ok {
			if path := fileAliases[pkgIdent.Name]; path != "" && idx.exports != nil {
				if t := idx.exports.funcs[path][fun.Sel.Name]; t != "" {
					return t
				}
			}
			// Same-package qualifier unlikely; fall through to method resolution.
		}
		recvCanon := idx.exprCanonical(fun.X, scope, fileAliases)
		if recvCanon != "" {
			return idx.methodReturn(recvCanon, fun.Sel.Name)
		}
	}
	return ""
}

func (idx *pkgTypeIndex) exprCanonical(expr ast.Expr, scope *mutScope, fileAliases map[string]string) string {
	expr = unwrapParenExpr(expr)
	switch e := expr.(type) {
	case *ast.Ident:
		if d := scope.lookup(e.Name); d != nil {
			return d.typ
		}
	case *ast.CallExpr:
		return idx.callReturnCanonical(e, scope, fileAliases)
	case *ast.SelectorExpr:
		if e.Sel == nil {
			return ""
		}
		base := idx.exprCanonical(e.X, scope, fileAliases)
		if base != "" {
			short := idx.shortTypeName(base)
			if fields, ok := idx.structFields[short]; ok {
				if t, ok := fields[e.Sel.Name]; ok {
					return t
				}
			}
			pkg := idx.packageOfCanonical(base)
			if idx.exports != nil && pkg != "" {
				if fields := idx.exports.fields[pkg][short]; fields != nil {
					if t, ok := fields[e.Sel.Name]; ok {
						return t
					}
				}
			}
		}
		// No field-name shortcuts: nested selectors named Executor require type evidence.
	case *ast.StarExpr:
		return idx.exprCanonical(e.X, scope, fileAliases)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return idx.exprCanonical(e.X, scope, fileAliases)
		}
	case *ast.IndexExpr:
		return idx.exprCanonical(e.X, scope, fileAliases)
	case *ast.CompositeLit:
		if e.Type != nil {
			return resolveCanonicalType(e.Type, idx.pkgPath, fileAliases, idx.aliases)
		}
	}
	return ""
}

func (idx *pkgTypeIndex) isActiveExpr(expr ast.Expr, scope *mutScope, fileAliases map[string]string) bool {
	expr = unwrapParenExpr(expr)
	switch e := expr.(type) {
	case *ast.Ident:
		if d := scope.lookup(e.Name); d != nil {
			if d.tracked {
				return true
			}
			return isApprovedActiveCanonical(d.typ)
		}
		return false
	case *ast.CallExpr:
		return isApprovedActiveCanonical(idx.callReturnCanonical(e, scope, fileAliases))
	case *ast.SelectorExpr:
		if isApprovedActiveCanonical(idx.exprCanonical(e, scope, fileAliases)) {
			return true
		}
		if root := receiverRootIdent(e.X); root != "" {
			if d := scope.lookup(root); d != nil && d.tracked {
				return true
			}
		}
		return idx.isActiveExpr(e.X, scope, fileAliases)
	case *ast.StarExpr:
		return idx.isActiveExpr(e.X, scope, fileAliases)
	case *ast.IndexExpr:
		return idx.isActiveExpr(e.X, scope, fileAliases)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return idx.isActiveExpr(e.X, scope, fileAliases)
		}
	}
	return false
}

func receiverRootIdent(expr ast.Expr) string {
	expr = unwrapParenExpr(expr)
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return receiverRootIdent(e.X)
	case *ast.StarExpr:
		return receiverRootIdent(e.X)
	case *ast.IndexExpr:
		return receiverRootIdent(e.X)
	default:
		return ""
	}
}

func mutationReceiverLabel(expr ast.Expr) string {
	expr = unwrapParenExpr(expr)
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if e.Sel == nil {
			return ""
		}
		base := mutationReceiverLabel(e.X)
		if base == "" {
			return e.Sel.Name
		}
		return base + "." + e.Sel.Name
	case *ast.CallExpr:
		switch fun := unwrapParenExpr(e.Fun).(type) {
		case *ast.Ident:
			return fun.Name + "()"
		case *ast.SelectorExpr:
			if fun.Sel == nil {
				return "()"
			}
			base := mutationReceiverLabel(fun.X)
			if base == "" {
				return fun.Sel.Name + "()"
			}
			return base + "." + fun.Sel.Name + "()"
		}
		return "()"
	case *ast.StarExpr:
		return mutationReceiverLabel(e.X)
	case *ast.IndexExpr:
		return mutationReceiverLabel(e.X) + "[]"
	}
	return ""
}

// mutDecl is one lexical declaration's active-runtime tracking state.
type mutDecl struct {
	typ     string
	tracked bool
}

// mutScope is a lexical scope frame; lookups walk parent frames by declaration
// identity (the mutDecl pointer), not by permanently tainting a name spelling.
type mutScope struct {
	parent *mutScope
	decls  map[string]*mutDecl
}

func newMutScope(parent *mutScope) *mutScope {
	return &mutScope{parent: parent, decls: map[string]*mutDecl{}}
}

func (s *mutScope) lookup(name string) *mutDecl {
	for cur := s; cur != nil; cur = cur.parent {
		if d, ok := cur.decls[name]; ok {
			return d
		}
	}
	return nil
}

func (s *mutScope) declare(name, typ string) *mutDecl {
	if s == nil || name == "" || name == "_" {
		return nil
	}
	d := &mutDecl{typ: typ, tracked: isApprovedActiveCanonical(typ)}
	s.decls[name] = d
	return d
}

func (d *mutDecl) applyType(typ string, forceTracked bool) {
	if d == nil {
		return
	}
	if typ != "" {
		d.typ = typ
		d.tracked = isApprovedActiveCanonical(typ)
		return
	}
	if forceTracked {
		d.tracked = true
	}
}

// forkMutEnv clones all visible declaration bindings into an independent root
// scope so branch/closure analysis cannot mutate parent mutDecl records.
// preexisting names are those present at fork time (shadows declared later are
// local to the fork and must not merge back).
func forkMutEnv(scope *mutScope) (*mutScope, map[string]struct{}) {
	env := newMutScope(nil)
	preexisting := map[string]struct{}{}
	seen := map[string]bool{}
	for cur := scope; cur != nil; cur = cur.parent {
		names := make([]string, 0, len(cur.decls))
		for name := range cur.decls {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if seen[name] {
				continue
			}
			seen[name] = true
			d := cur.decls[name]
			preexisting[name] = struct{}{}
			env.decls[name] = &mutDecl{typ: d.typ, tracked: d.tracked}
		}
	}
	return env, preexisting
}

// snapshotMutEnv copies the current binding state of preexisting names from scope
// into a fresh root (used as the false/entry path when a branch may be skipped).
func snapshotMutEnv(scope *mutScope, preexisting map[string]struct{}) *mutScope {
	env := newMutScope(nil)
	names := make([]string, 0, len(preexisting))
	for name := range preexisting {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if d := scope.lookup(name); d != nil {
			env.decls[name] = &mutDecl{typ: d.typ, tracked: d.tracked}
		}
	}
	return env
}

// mergeMutEnvs conservatively merges forked branch states back into target for
// preexisting names only: active if active on any reachable branch; inactive
// only when every reachable branch proves inactive. Shadow declarations in
// branches are ignored because they never appear in branch.decls at the fork root
// after being overwritten only in child frames — fork-root copies stay distinct.
func mergeMutEnvs(target *mutScope, preexisting map[string]struct{}, branches ...*mutScope) {
	if target == nil || len(preexisting) == 0 || len(branches) == 0 {
		return
	}
	names := make([]string, 0, len(preexisting))
	for name := range preexisting {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		orig := target.lookup(name)
		if orig == nil {
			continue
		}
		anyActive := false
		activeTyp := ""
		inactiveTyp := ""
		for _, b := range branches {
			if b == nil {
				continue
			}
			d, ok := b.decls[name]
			if !ok || d == nil {
				continue
			}
			if d.tracked {
				anyActive = true
				if activeTyp == "" && d.typ != "" {
					activeTyp = d.typ
				}
			} else if inactiveTyp == "" && d.typ != "" {
				inactiveTyp = d.typ
			}
		}
		if anyActive {
			orig.tracked = true
			if activeTyp != "" {
				orig.typ = activeTyp
			}
		} else {
			orig.tracked = false
			if inactiveTyp != "" {
				orig.typ = inactiveTyp
			}
		}
	}
}

func seedMutFieldList(scope *mutScope, fields *ast.FieldList, idx *pkgTypeIndex, fileAliases map[string]string) {
	if scope == nil || fields == nil {
		return
	}
	for _, field := range fields.List {
		typ := resolveDeclType(field.Type, idx, fileAliases)
		for _, name := range field.Names {
			if name != nil {
				scope.declare(name.Name, typ)
			}
		}
	}
}

func findActiveRuntimeMutationSetters(fset *token.FileSet, f *ast.File, idx *pkgTypeIndex, fileAliases map[string]string) []string {
	var out []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		out = append(out, findActiveRuntimeMutationsInFunc(fset, fn, idx, fileAliases)...)
	}
	return out
}

func resolveDeclType(expr ast.Expr, idx *pkgTypeIndex, fileAliases map[string]string) string {
	typ := resolveCanonicalType(expr, idx.pkgPath, fileAliases, idx.aliases)
	if local := typeNameHint(expr); local != "" && !strings.Contains(local, ".") {
		if c, ok := idx.aliases[local]; ok {
			typ = c
		}
	}
	if local := recvTypeLocalName(expr); local != "" {
		if c, ok := idx.aliases[local]; ok {
			typ = c
		}
	}
	if base, ok := idx.aliases[idx.shortTypeName(typ)]; ok && (typ == idx.shortTypeName(typ) || strings.HasSuffix(typ, "."+idx.shortTypeName(typ))) {
		_ = base
	}
	return typ
}

func findActiveRuntimeMutationsInFunc(fset *token.FileSet, fn *ast.FuncDecl, idx *pkgTypeIndex, fileAliases map[string]string) []string {
	root := newMutScope(nil)
	if fn.Recv != nil {
		seedMutFieldList(root, fn.Recv, idx, fileAliases)
	}
	if fn.Type != nil {
		seedMutFieldList(root, fn.Type.Params, idx, fileAliases)
	}

	var out []string
	analyzeMutBlock(fset, fn.Body, root, idx, fileAliases, &out)
	return out
}

func analyzeMutBlock(fset *token.FileSet, block *ast.BlockStmt, scope *mutScope, idx *pkgTypeIndex, fileAliases map[string]string, out *[]string) {
	if block == nil {
		return
	}
	child := newMutScope(scope)
	for _, stmt := range block.List {
		analyzeMutStmt(fset, stmt, child, idx, fileAliases, out)
	}
}

func analyzeMutStmt(fset *token.FileSet, stmt ast.Stmt, scope *mutScope, idx *pkgTypeIndex, fileAliases map[string]string, out *[]string) {
	if stmt == nil {
		return
	}
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		analyzeMutBlock(fset, s, scope, idx, fileAliases, out)
	case *ast.DeclStmt:
		if gen, ok := s.Decl.(*ast.GenDecl); ok {
			analyzeMutGenDecl(fset, gen, scope, idx, fileAliases, out)
		}
	case *ast.AssignStmt:
		analyzeMutAssign(fset, s, scope, idx, fileAliases, out)
	case *ast.IncDecStmt:
		if hit := mutationLHS(fset, s.X, scope, idx, fileAliases); hit != "" {
			*out = append(*out, hit)
		}
		walkMutExpr(fset, s.X, scope, idx, fileAliases, out)
	case *ast.ExprStmt:
		walkMutExpr(fset, s.X, scope, idx, fileAliases, out)
	case *ast.IfStmt:
		ifScope := newMutScope(scope)
		if s.Init != nil {
			analyzeMutStmt(fset, s.Init, ifScope, idx, fileAliases, out)
		}
		walkMutExpr(fset, s.Cond, ifScope, idx, fileAliases, out)
		_, preexisting := forkMutEnv(ifScope)
		thenEnv, _ := forkMutEnv(ifScope)
		analyzeMutBlock(fset, s.Body, thenEnv, idx, fileAliases, out)
		branches := []*mutScope{thenEnv}
		if s.Else != nil {
			elseEnv, _ := forkMutEnv(ifScope)
			analyzeMutStmt(fset, s.Else, elseEnv, idx, fileAliases, out)
			branches = append(branches, elseEnv)
		} else {
			branches = append(branches, snapshotMutEnv(ifScope, preexisting))
		}
		mergeMutEnvs(ifScope, preexisting, branches...)
	case *ast.ForStmt:
		forScope := newMutScope(scope)
		if s.Init != nil {
			analyzeMutStmt(fset, s.Init, forScope, idx, fileAliases, out)
		}
		walkMutExpr(fset, s.Cond, forScope, idx, fileAliases, out)
		_, preexisting := forkMutEnv(forScope)
		bodyEnv, _ := forkMutEnv(forScope)
		analyzeMutBlock(fset, s.Body, bodyEnv, idx, fileAliases, out)
		if s.Post != nil {
			analyzeMutStmt(fset, s.Post, bodyEnv, idx, fileAliases, out)
		}
		if s.Cond == nil {
			// for { } / for ;;  body executes at least once when the statement runs.
			mergeMutEnvs(forScope, preexisting, bodyEnv)
		} else {
			mergeMutEnvs(forScope, preexisting, snapshotMutEnv(forScope, preexisting), bodyEnv)
		}
	case *ast.RangeStmt:
		rangeScope := newMutScope(scope)
		walkMutExpr(fset, s.X, scope, idx, fileAliases, out)
		if s.Tok == token.DEFINE {
			if id, ok := s.Key.(*ast.Ident); ok {
				rangeScope.declare(id.Name, "")
			}
			if id, ok := s.Value.(*ast.Ident); ok {
				rangeScope.declare(id.Name, "")
			}
		} else {
			walkMutExpr(fset, s.Key, rangeScope, idx, fileAliases, out)
			walkMutExpr(fset, s.Value, rangeScope, idx, fileAliases, out)
		}
		_, preexisting := forkMutEnv(rangeScope)
		bodyEnv, _ := forkMutEnv(rangeScope)
		analyzeMutBlock(fset, s.Body, bodyEnv, idx, fileAliases, out)
		// Range may execute zero times; always merge entry with body exit.
		mergeMutEnvs(rangeScope, preexisting, snapshotMutEnv(rangeScope, preexisting), bodyEnv)
	case *ast.SwitchStmt:
		swScope := newMutScope(scope)
		if s.Init != nil {
			analyzeMutStmt(fset, s.Init, swScope, idx, fileAliases, out)
		}
		walkMutExpr(fset, s.Tag, swScope, idx, fileAliases, out)
		_, preexisting := forkMutEnv(swScope)
		var branches []*mutScope
		hasDefault := false
		if s.Body != nil {
			for _, clause := range s.Body.List {
				cc, ok := clause.(*ast.CaseClause)
				if !ok {
					continue
				}
				if cc.List == nil {
					hasDefault = true
				}
				caseEnv, _ := forkMutEnv(swScope)
				for _, e := range cc.List {
					walkMutExpr(fset, e, caseEnv, idx, fileAliases, out)
				}
				for _, cs := range cc.Body {
					analyzeMutStmt(fset, cs, caseEnv, idx, fileAliases, out)
				}
				branches = append(branches, caseEnv)
			}
		}
		if !hasDefault {
			branches = append(branches, snapshotMutEnv(swScope, preexisting))
		}
		if len(branches) > 0 {
			mergeMutEnvs(swScope, preexisting, branches...)
		}
	case *ast.TypeSwitchStmt:
		swScope := newMutScope(scope)
		if s.Init != nil {
			analyzeMutStmt(fset, s.Init, swScope, idx, fileAliases, out)
		}
		if s.Assign != nil {
			analyzeMutStmt(fset, s.Assign, swScope, idx, fileAliases, out)
		}
		_, preexisting := forkMutEnv(swScope)
		var branches []*mutScope
		hasDefault := false
		if s.Body != nil {
			for _, clause := range s.Body.List {
				cc, ok := clause.(*ast.CaseClause)
				if !ok {
					continue
				}
				if cc.List == nil {
					hasDefault = true
				}
				caseEnv, _ := forkMutEnv(swScope)
				for _, cs := range cc.Body {
					analyzeMutStmt(fset, cs, caseEnv, idx, fileAliases, out)
				}
				branches = append(branches, caseEnv)
			}
		}
		if !hasDefault {
			branches = append(branches, snapshotMutEnv(swScope, preexisting))
		}
		if len(branches) > 0 {
			mergeMutEnvs(swScope, preexisting, branches...)
		}
	case *ast.SelectStmt:
		if s.Body == nil {
			return
		}
		_, preexisting := forkMutEnv(scope)
		var branches []*mutScope
		hasDefault := false
		for _, clause := range s.Body.List {
			cc, ok := clause.(*ast.CommClause)
			if !ok {
				continue
			}
			if cc.Comm == nil {
				hasDefault = true
			}
			commEnv, _ := forkMutEnv(scope)
			if cc.Comm != nil {
				analyzeMutStmt(fset, cc.Comm, commEnv, idx, fileAliases, out)
			}
			for _, cs := range cc.Body {
				analyzeMutStmt(fset, cs, commEnv, idx, fileAliases, out)
			}
			branches = append(branches, commEnv)
		}
		if !hasDefault {
			branches = append(branches, snapshotMutEnv(scope, preexisting))
		}
		if len(branches) > 0 {
			mergeMutEnvs(scope, preexisting, branches...)
		}
	case *ast.GoStmt:
		if s.Call != nil {
			walkMutExpr(fset, s.Call, scope, idx, fileAliases, out)
		}
	case *ast.DeferStmt:
		if s.Call != nil {
			walkMutExpr(fset, s.Call, scope, idx, fileAliases, out)
		}
	case *ast.ReturnStmt:
		for _, r := range s.Results {
			walkMutExpr(fset, r, scope, idx, fileAliases, out)
		}
	case *ast.SendStmt:
		walkMutExpr(fset, s.Chan, scope, idx, fileAliases, out)
		walkMutExpr(fset, s.Value, scope, idx, fileAliases, out)
	case *ast.LabeledStmt:
		analyzeMutStmt(fset, s.Stmt, scope, idx, fileAliases, out)
	case *ast.CaseClause:
		caseScope := newMutScope(scope)
		for _, e := range s.List {
			walkMutExpr(fset, e, caseScope, idx, fileAliases, out)
		}
		for _, cs := range s.Body {
			analyzeMutStmt(fset, cs, caseScope, idx, fileAliases, out)
		}
	case *ast.CommClause:
		commScope := newMutScope(scope)
		if s.Comm != nil {
			analyzeMutStmt(fset, s.Comm, commScope, idx, fileAliases, out)
		}
		for _, cs := range s.Body {
			analyzeMutStmt(fset, cs, commScope, idx, fileAliases, out)
		}
	}
}

func analyzeMutGenDecl(fset *token.FileSet, gen *ast.GenDecl, scope *mutScope, idx *pkgTypeIndex, fileAliases map[string]string, out *[]string) {
	if gen.Tok != token.VAR && gen.Tok != token.CONST {
		return
	}
	for _, spec := range gen.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		var declType string
		if vs.Type != nil {
			declType = resolveDeclType(vs.Type, idx, fileAliases)
		}
		for i, name := range vs.Names {
			if name == nil {
				continue
			}
			typ := declType
			trackedFromRHS := false
			if i < len(vs.Values) {
				walkMutExpr(fset, vs.Values[i], scope, idx, fileAliases, out)
				rhsTyp, force := typeFromMutExpr(vs.Values[i], idx, scope, fileAliases)
				if rhsTyp != "" {
					typ = rhsTyp
				} else if force {
					trackedFromRHS = true
				}
			}
			d := scope.declare(name.Name, typ)
			if d != nil && trackedFromRHS && typ == "" {
				d.tracked = true
			}
		}
	}
}

func analyzeMutAssign(fset *token.FileSet, assign *ast.AssignStmt, scope *mutScope, idx *pkgTypeIndex, fileAliases map[string]string, out *[]string) {
	for _, rhs := range assign.Rhs {
		walkMutExpr(fset, rhs, scope, idx, fileAliases, out)
	}
	for _, lhs := range assign.Lhs {
		if hit := mutationLHS(fset, lhs, scope, idx, fileAliases); hit != "" {
			*out = append(*out, hit)
		}
	}

	if len(assign.Lhs) != len(assign.Rhs) {
		// Multi-value assignments without 1:1 pairing: still honor DEFINE
		// declarations without type evidence when possible.
		if assign.Tok == token.DEFINE {
			for _, lhs := range assign.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name == "_" {
					continue
				}
				if _, inCurrent := scope.decls[id.Name]; !inCurrent {
					scope.declare(id.Name, "")
				}
			}
		}
		return
	}

	for i, lhs := range assign.Lhs {
		id, ok := lhs.(*ast.Ident)
		if !ok || id.Name == "_" {
			continue
		}
		rhsTyp, forceTracked := typeFromMutExpr(assign.Rhs[i], idx, scope, fileAliases)
		if assign.Tok == token.DEFINE {
			if _, inCurrent := scope.decls[id.Name]; inCurrent {
				scope.decls[id.Name].applyType(rhsTyp, forceTracked)
			} else {
				d := scope.declare(id.Name, rhsTyp)
				if d != nil && forceTracked && rhsTyp == "" {
					d.tracked = true
				}
			}
			continue
		}
		if d := scope.lookup(id.Name); d != nil {
			if rhsTyp != "" || forceTracked {
				d.applyType(rhsTyp, forceTracked)
			}
		}
	}
}

func typeFromMutExpr(rhs ast.Expr, idx *pkgTypeIndex, scope *mutScope, fileAliases map[string]string) (typ string, forceTracked bool) {
	rhs = unwrapParenExpr(rhs)
	switch e := rhs.(type) {
	case *ast.Ident:
		if d := scope.lookup(e.Name); d != nil {
			return d.typ, d.tracked && d.typ == ""
		}
	case *ast.CallExpr:
		if t := idx.callReturnCanonical(e, scope, fileAliases); t != "" {
			return t, false
		}
	case *ast.SelectorExpr:
		if t := idx.exprCanonical(e, scope, fileAliases); t != "" {
			return t, false
		}
		if root := receiverRootIdent(e.X); root != "" {
			if d := scope.lookup(root); d != nil && d.tracked {
				return d.typ, d.typ == ""
			}
		}
	case *ast.StarExpr:
		if t := idx.exprCanonical(e.X, scope, fileAliases); t != "" {
			return t, false
		}
		if root := receiverRootIdent(e.X); root != "" {
			if d := scope.lookup(root); d != nil && d.tracked {
				return d.typ, true
			}
		}
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return typeFromMutExpr(e.X, idx, scope, fileAliases)
		}
	case *ast.CompositeLit:
		if e.Type != nil {
			t := resolveCanonicalType(e.Type, idx.pkgPath, fileAliases, idx.aliases)
			if local := typeNameHint(e.Type); local != "" && !strings.Contains(local, ".") {
				if c, ok := idx.aliases[local]; ok {
					t = c
				}
			}
			return t, false
		}
	case *ast.IndexExpr:
		if t := idx.exprCanonical(e, scope, fileAliases); t != "" {
			return t, false
		}
	}
	return "", false
}

func walkMutExpr(fset *token.FileSet, expr ast.Expr, scope *mutScope, idx *pkgTypeIndex, fileAliases map[string]string, out *[]string) {
	if expr == nil {
		return
	}
	switch e := expr.(type) {
	case *ast.FuncLit:
		// Analyze the body against a cloned capture environment so mutations
		// inside the closure are reported, but assignments do not overwrite
		// outer post-declaration state.
		litEnv, _ := forkMutEnv(scope)
		if e.Type != nil {
			seedMutFieldList(litEnv, e.Type.Params, idx, fileAliases)
			seedMutFieldList(litEnv, e.Type.Results, idx, fileAliases)
		}
		analyzeMutBlock(fset, e.Body, litEnv, idx, fileAliases, out)
		return
	case *ast.CallExpr:
		if hit := mutationBuiltinCall(fset, e, scope, idx, fileAliases); hit != "" {
			*out = append(*out, hit)
		}
		if sel, ok := unwrapParenExpr(e.Fun).(*ast.SelectorExpr); ok && sel.Sel != nil {
			name := sel.Sel.Name
			if isMutatingMethodName(name) && idx.isActiveExpr(sel.X, scope, fileAliases) {
				label := mutationReceiverLabel(sel.X)
				if label == "" {
					label = receiverRootName(sel.X)
				}
				*out = append(*out, formatPos(fset, e.Pos())+": "+label+"."+name)
			}
		}
		walkMutExpr(fset, e.Fun, scope, idx, fileAliases, out)
		for _, a := range e.Args {
			walkMutExpr(fset, a, scope, idx, fileAliases, out)
		}
		return
	case *ast.ParenExpr:
		walkMutExpr(fset, e.X, scope, idx, fileAliases, out)
	case *ast.SelectorExpr:
		walkMutExpr(fset, e.X, scope, idx, fileAliases, out)
	case *ast.StarExpr:
		walkMutExpr(fset, e.X, scope, idx, fileAliases, out)
	case *ast.UnaryExpr:
		walkMutExpr(fset, e.X, scope, idx, fileAliases, out)
	case *ast.BinaryExpr:
		walkMutExpr(fset, e.X, scope, idx, fileAliases, out)
		walkMutExpr(fset, e.Y, scope, idx, fileAliases, out)
	case *ast.IndexExpr:
		walkMutExpr(fset, e.X, scope, idx, fileAliases, out)
		walkMutExpr(fset, e.Index, scope, idx, fileAliases, out)
	case *ast.IndexListExpr:
		walkMutExpr(fset, e.X, scope, idx, fileAliases, out)
		for _, i := range e.Indices {
			walkMutExpr(fset, i, scope, idx, fileAliases, out)
		}
	case *ast.SliceExpr:
		walkMutExpr(fset, e.X, scope, idx, fileAliases, out)
		walkMutExpr(fset, e.Low, scope, idx, fileAliases, out)
		walkMutExpr(fset, e.High, scope, idx, fileAliases, out)
		walkMutExpr(fset, e.Max, scope, idx, fileAliases, out)
	case *ast.TypeAssertExpr:
		walkMutExpr(fset, e.X, scope, idx, fileAliases, out)
	case *ast.CompositeLit:
		for _, elt := range e.Elts {
			walkMutExpr(fset, elt, scope, idx, fileAliases, out)
		}
	case *ast.KeyValueExpr:
		walkMutExpr(fset, e.Key, scope, idx, fileAliases, out)
		walkMutExpr(fset, e.Value, scope, idx, fileAliases, out)
	case *ast.ArrayType:
		walkMutExpr(fset, e.Len, scope, idx, fileAliases, out)
		walkMutExpr(fset, e.Elt, scope, idx, fileAliases, out)
	case *ast.MapType:
		walkMutExpr(fset, e.Key, scope, idx, fileAliases, out)
		walkMutExpr(fset, e.Value, scope, idx, fileAliases, out)
	}
}

func mutationBuiltinCall(fset *token.FileSet, call *ast.CallExpr, scope *mutScope, idx *pkgTypeIndex, fileAliases map[string]string) string {
	id, ok := unwrapParenExpr(call.Fun).(*ast.Ident)
	if !ok {
		return ""
	}
	switch id.Name {
	case "delete", "clear":
		if len(call.Args) >= 1 && isActiveContainerDest(call.Args[0], scope, idx, fileAliases) {
			return formatPos(fset, call.Pos()) + ": " + id.Name + "(" + mutationReceiverLabel(call.Args[0]) + ")"
		}
	case "copy":
		if len(call.Args) >= 1 && isActiveContainerDest(call.Args[0], scope, idx, fileAliases) {
			return formatPos(fset, call.Pos()) + ": copy(" + mutationReceiverLabel(call.Args[0]) + ")"
		}
	}
	return ""
}

func isActiveContainerDest(expr ast.Expr, scope *mutScope, idx *pkgTypeIndex, fileAliases map[string]string) bool {
	expr = unwrapParenExpr(expr)
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		if root := receiverRootIdent(e.X); root != "" {
			if d := scope.lookup(root); d != nil && d.tracked {
				return true
			}
		}
		return idx.isActiveExpr(e.X, scope, fileAliases)
	case *ast.IndexExpr:
		return isActiveContainerDest(e.X, scope, idx, fileAliases)
	case *ast.Ident:
		if d := scope.lookup(e.Name); d != nil {
			return d.tracked
		}
		return false
	case *ast.StarExpr:
		return isActiveContainerDest(e.X, scope, idx, fileAliases)
	}
	return false
}

func mutationLHS(fset *token.FileSet, lhs ast.Expr, scope *mutScope, idx *pkgTypeIndex, fileAliases map[string]string) string {
	lhs = unwrapParenExpr(lhs)
	switch e := lhs.(type) {
	case *ast.SelectorExpr:
		if e.Sel == nil {
			return ""
		}
		// Require tracked/active owner evidence — never treat a nested field
		// merely named Executor/Exec as active by name alone.
		if root := receiverRootIdent(e.X); root != "" {
			if d := scope.lookup(root); d != nil && d.tracked {
				return formatPos(fset, lhs.Pos()) + ": " + root + "." + e.Sel.Name + "="
			}
		}
		if idx.isActiveExpr(e.X, scope, fileAliases) {
			label := mutationReceiverLabel(e.X)
			if label == "" {
				label = receiverRootIdent(e.X)
			}
			return formatPos(fset, lhs.Pos()) + ": " + label + "." + e.Sel.Name + "="
		}
	case *ast.IndexExpr:
		if root := receiverRootIdent(e.X); root != "" {
			if d := scope.lookup(root); d != nil && d.tracked {
				return formatPos(fset, lhs.Pos()) + ": " + root + "[]="
			}
		}
		if sel, ok := unwrapParenExpr(e.X).(*ast.SelectorExpr); ok && sel.Sel != nil {
			if root := receiverRootIdent(sel.X); root != "" {
				if d := scope.lookup(root); d != nil && d.tracked {
					return formatPos(fset, lhs.Pos()) + ": " + root + "." + sel.Sel.Name + "[]="
				}
			}
		}
	case *ast.StarExpr:
		if root := receiverRootIdent(e.X); root != "" {
			if d := scope.lookup(root); d != nil && d.tracked {
				return formatPos(fset, lhs.Pos()) + ": *" + root + "="
			}
		}
	}
	return ""
}

func receiverRootName(expr ast.Expr) string {
	if id := receiverRootIdent(expr); id != "" {
		return id
	}
	expr = unwrapParenExpr(expr)
	switch e := expr.(type) {
	case *ast.CallExpr:
		return receiverRootName(e.Fun)
	case *ast.SelectorExpr:
		return receiverRootName(e.X)
	case *ast.StarExpr:
		return receiverRootName(e.X)
	}
	return ""
}

func isWatcherImportPath(path string) bool {
	return strings.Contains(path, "fsnotify") ||
		strings.Contains(path, "rjeczalik/notify") ||
		strings.HasSuffix(path, "/watcher") ||
		strings.Contains(path, "/notify")
}

func isLegitimateRefreshAllowPath(filename string) bool {
	path := filepath.ToSlash(filename)
	allow := []string{
		"internal/infra/runtimebundle/modelcatalog_refresh_loop.go",
		"internal/infra/runtimebundle/modelregistry_refresh_loop.go",
		"internal/core/terminalwork/app/ticker.go",
		"internal/core/terminalwork/app/processor.go",
	}
	for _, a := range allow {
		if strings.HasSuffix(path, a) {
			return true
		}
	}
	return false
}

func isConfigSourceAdapterPath(filename string) bool {
	p := filepath.ToSlash(filename)
	return strings.Contains(p, "/internal/infra/configsource/") ||
		strings.HasPrefix(p, "internal/infra/configsource/") ||
		strings.HasSuffix(p, "/internal/infra/configsource") ||
		p == "internal/infra/configsource"
}

func isTimePollName(name string) bool {
	switch name {
	case "NewTicker", "Tick", "After", "NewTimer", "Sleep", "AfterFunc":
		return true
	default:
		return false
	}
}

func isTimerValueCreateName(name string) bool {
	switch name {
	case "NewTicker", "NewTimer":
		return true
	default:
		return false
	}
}

func isTimerChannelCreateName(name string) bool {
	return name == "Tick"
}

func isStatProbeName(name string) bool {
	switch name {
	case "Stat", "Lstat", "ReadDir", "ReadFile", "Open", "OpenFile":
		return true
	default:
		return false
	}
}

func timeCallName(call *ast.CallExpr, aliasToPath map[string]string, aliases funcValueAliases) (string, bool) {
	if label, ok := callViaFuncAlias(call, aliases); ok {
		switch {
		case strings.HasSuffix(label, "time.NewTicker"):
			return "NewTicker", true
		case strings.HasSuffix(label, "time.Tick"):
			return "Tick", true
		case strings.HasSuffix(label, "time.After"):
			return "After", true
		case strings.HasSuffix(label, "time.NewTimer"):
			return "NewTimer", true
		case strings.HasSuffix(label, "time.Sleep"):
			return "Sleep", true
		case strings.HasSuffix(label, "time.AfterFunc"):
			return "AfterFunc", true
		}
		return "", false
	}
	recv, name, ok := callSelector(call)
	if !ok || !isTimePollName(name) {
		return "", false
	}
	path := resolveImportPath(aliasToPath, recv)
	if !pathHasSuffix(path, "time") {
		return "", false
	}
	return name, true
}

func osProbeCallName(call *ast.CallExpr, aliasToPath map[string]string, aliases funcValueAliases) (string, bool) {
	if label, ok := callViaFuncAlias(call, aliases); ok {
		switch {
		case strings.HasSuffix(label, "os.Stat"):
			return "Stat", true
		case strings.HasSuffix(label, "os.Lstat"):
			return "Lstat", true
		case strings.HasSuffix(label, "os.ReadDir"):
			return "ReadDir", true
		case strings.HasSuffix(label, "os.ReadFile"):
			return "ReadFile", true
		case strings.HasSuffix(label, "os.OpenFile"):
			return "OpenFile", true
		case strings.HasSuffix(label, "os.Open"):
			return "Open", true
		}
		return "", false
	}
	recv, name, ok := callSelector(call)
	if !ok || !isStatProbeName(name) {
		return "", false
	}
	path := resolveImportPath(aliasToPath, recv)
	if !pathHasSuffix(path, "os") {
		return "", false
	}
	return name, true
}

func looksLikeConfigPath(expr ast.Expr) bool {
	lit, ok := unwrapParenExpr(expr).(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s := strings.ToLower(strings.Trim(lit.Value, `"`))
	if s == "" {
		return false
	}
	// Cache/blob/tmp/model/catalog paths take explicit negative precedence over
	// positive config tokens (project name alone is not config evidence).
	if looksLikeNonConfigPathString(s) {
		return false
	}
	keys := []string{
		"config", ".yaml", ".yml", ".toml", ".json",
		"/etc/", "reload", "proxy",
	}
	for _, k := range keys {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

func looksLikeConfigPathName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	if n == "path" || n == "filepath" || n == "filename" || n == "source" {
		return true
	}
	return strings.Contains(n, "config") ||
		strings.HasSuffix(n, "path") ||
		strings.HasSuffix(n, "file") ||
		strings.Contains(n, "reload")
}

func looksLikeNonConfigPathString(s string) bool {
	if s == "" {
		return false
	}
	keys := []string{
		"/var/cache", "cache/", "/cache",
		"tmp/", "/tmp/", "temp/",
		"blob", "model", "catalog",
	}
	for _, k := range keys {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

func looksLikeCacheOrNonConfigPath(expr ast.Expr) bool {
	lit, ok := unwrapParenExpr(expr).(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s := strings.ToLower(strings.Trim(lit.Value, `"`))
	if s == "" {
		return false
	}
	if looksLikeNonConfigPathString(s) {
		return true
	}
	// Any other concrete string that is not a config path clears evidence.
	return !looksLikeConfigPath(lit)
}

// pathDecl tracks whether a lexical declaration currently holds config-path evidence.
type pathDecl struct {
	isConfig bool
	id       int // lexical declaration identity (stable across forks)
}

// pathDeclSeq allocates declaration identities within a scan. Tests may run
// scanners in parallel, so allocation is atomic.
var pathDeclSeq atomic.Int64

type pathScope struct {
	parent *pathScope
	decls  map[string]*pathDecl
}

func newPathScope(parent *pathScope) *pathScope {
	return &pathScope{parent: parent, decls: map[string]*pathDecl{}}
}

func (s *pathScope) lookup(name string) *pathDecl {
	for cur := s; cur != nil; cur = cur.parent {
		if d, ok := cur.decls[name]; ok {
			return d
		}
	}
	return nil
}

func (s *pathScope) lookupByID(id int) *pathDecl {
	if id == 0 {
		return nil
	}
	for cur := s; cur != nil; cur = cur.parent {
		for _, d := range cur.decls {
			if d != nil && d.id == id {
				return d
			}
		}
	}
	return nil
}

func (s *pathScope) declare(name string, isConfig bool) *pathDecl {
	if s == nil || name == "" || name == "_" {
		return nil
	}
	id := int(pathDeclSeq.Add(1))
	d := &pathDecl{isConfig: isConfig, id: id}
	s.decls[name] = d
	return d
}

func (d *pathDecl) setConfig(isConfig bool) {
	if d != nil {
		d.isConfig = isConfig
	}
}

func forkPathEnv(scope *pathScope) (*pathScope, map[string]struct{}) {
	env := newPathScope(nil)
	preexisting := map[string]struct{}{}
	seen := map[string]bool{}
	for cur := scope; cur != nil; cur = cur.parent {
		names := make([]string, 0, len(cur.decls))
		for name := range cur.decls {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if seen[name] {
				continue
			}
			seen[name] = true
			d := cur.decls[name]
			preexisting[name] = struct{}{}
			env.decls[name] = &pathDecl{isConfig: d.isConfig, id: d.id}
		}
	}
	return env, preexisting
}

func snapshotPathEnv(scope *pathScope, preexisting map[string]struct{}) *pathScope {
	env := newPathScope(nil)
	names := make([]string, 0, len(preexisting))
	for name := range preexisting {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if d := scope.lookup(name); d != nil {
			env.decls[name] = &pathDecl{isConfig: d.isConfig, id: d.id}
		}
	}
	return env
}

// snapshotPathEnvByID captures config bits for entry declaration identities,
// ignoring same-spelling shadows introduced later in the scope chain.
func snapshotPathEnvByID(scope *pathScope, entryIDs map[string]int) *pathScope {
	env := newPathScope(nil)
	names := make([]string, 0, len(entryIDs))
	for name := range entryIDs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		id := entryIDs[name]
		d := scope.lookupByID(id)
		if d == nil {
			continue
		}
		env.decls[name] = &pathDecl{isConfig: d.isConfig, id: id}
	}
	return env
}

func visiblePathEntryIDs(scope *pathScope) map[string]int {
	ids := map[string]int{}
	seen := map[string]bool{}
	for cur := scope; cur != nil; cur = cur.parent {
		names := make([]string, 0, len(cur.decls))
		for name := range cur.decls {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if seen[name] {
				continue
			}
			seen[name] = true
			if d := cur.decls[name]; d != nil {
				ids[name] = d.id
			}
		}
	}
	return ids
}

// pathRelevantEntryIDs restricts abstract loop states to declarations that can
// affect config-path evidence: config-tainted at entry, path-like names, or
// idents assigned/probed inside the loop body.
func pathRelevantEntryIDs(entry *pathScope, body *ast.BlockStmt, aliasToPath map[string]string, aliases funcValueAliases) map[string]int {
	all := visiblePathEntryIDs(entry)
	relevant := map[string]bool{}
	for name, id := range all {
		if looksLikeConfigPathName(name) {
			relevant[name] = true
			continue
		}
		if d := entry.lookupByID(id); d != nil && d.isConfig {
			relevant[name] = true
		}
	}
	if body != nil {
		ast.Inspect(body, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.AssignStmt:
				for _, lhs := range s.Lhs {
					id, ok := unwrapParenExpr(lhs).(*ast.Ident)
					if !ok || id.Name == "" || id.Name == "_" {
						continue
					}
					if _, ok := all[id.Name]; ok {
						// Only track outer entry decls, not fresh body locals.
						for _, rhs := range s.Rhs {
							if looksLikeCacheOrNonConfigPath(rhs) || looksLikeConfigPath(rhs) {
								relevant[id.Name] = true
							}
							if rid, ok := unwrapParenExpr(rhs).(*ast.Ident); ok {
								if relevant[rid.Name] || (entry.lookup(rid.Name) != nil && entry.lookup(rid.Name).isConfig) {
									relevant[id.Name] = true
								}
							}
						}
					}
				}
			case *ast.CallExpr:
				if _, ok := osProbeCallName(s, aliasToPath, aliases); !ok {
					return true
				}
				for _, arg := range s.Args {
					if id, ok := unwrapParenExpr(arg).(*ast.Ident); ok {
						if _, ok := all[id.Name]; ok {
							relevant[id.Name] = true
						}
					}
				}
			}
			return true
		})
	}
	out := map[string]int{}
	for name := range relevant {
		if id, ok := all[name]; ok {
			out[name] = id
		}
	}
	return out
}

func dedupePathScopesByIDs(states []*pathScope, ids map[string]int, maxStates int) ([]*pathScope, error) {
	if len(states) == 0 {
		return states, nil
	}
	seen := map[string]bool{}
	out := make([]*pathScope, 0, len(states))
	for _, st := range states {
		if st == nil {
			continue
		}
		key := absPathStateKey(st, ids)
		if seen[key] {
			continue
		}
		seen[key] = true
		// Keep the full scope so body-local declarations remain visible for
		// later statements in this iteration; only the key is projected.
		out = append(out, st)
	}
	if maxStates > 0 && len(out) > maxStates {
		return nil, fmt.Errorf("path abstract-state explosion (%d > %d)", len(out), maxStates)
	}
	return out, nil
}

func absPathStateKey(scope *pathScope, entryIDs map[string]int) string {
	names := make([]string, 0, len(entryIDs))
	for name := range entryIDs {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte('=')
		cfg := false
		if d := scope.lookupByID(entryIDs[name]); d != nil {
			cfg = d.isConfig
		}
		if cfg {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
		b.WriteByte(';')
	}
	return b.String()
}

// mergePathEnvs is conservative for detection: config evidence survives if any
// reachable branch still carries it; cleared only when every branch clears it.
func mergePathEnvs(target *pathScope, preexisting map[string]struct{}, branches ...*pathScope) {
	if target == nil || len(preexisting) == 0 || len(branches) == 0 {
		return
	}
	names := make([]string, 0, len(preexisting))
	for name := range preexisting {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		orig := target.lookup(name)
		if orig == nil {
			continue
		}
		anyConfig := false
		for _, b := range branches {
			if b == nil {
				continue
			}
			d, ok := b.decls[name]
			if ok && d != nil && d.isConfig {
				anyConfig = true
				break
			}
		}
		orig.isConfig = anyConfig
	}
}

// configPathPkgIndex holds package-aware statically resolvable config-path evidence.
type configPathPkgIndex struct {
	pkgVals    map[string]bool            // package-level const/var
	fields     map[string]map[string]bool // type -> field -> config
	funcParams map[string]map[string]bool // func/method identity -> param name -> config
	adapter    bool                       // package is configsource adapter
}

func newConfigPathPkgIndex() *configPathPkgIndex {
	return &configPathPkgIndex{
		pkgVals:    map[string]bool{},
		fields:     map[string]map[string]bool{},
		funcParams: map[string]map[string]bool{},
	}
}

func (idx *configPathPkgIndex) markField(typeName, field string) {
	if idx == nil || typeName == "" || field == "" {
		return
	}
	m := idx.fields[typeName]
	if m == nil {
		m = map[string]bool{}
		idx.fields[typeName] = m
	}
	m[field] = true
}

func (idx *configPathPkgIndex) fieldIsConfig(typeName, field string) bool {
	if idx == nil {
		return false
	}
	if idx.fields[typeName][field] {
		return true
	}
	if idx.adapter && looksLikeConfigPathName(field) {
		return true
	}
	return false
}

func (idx *configPathPkgIndex) markFuncParam(fnID, param string) {
	if idx == nil || fnID == "" || param == "" || param == "_" {
		return
	}
	m := idx.funcParams[fnID]
	if m == nil {
		m = map[string]bool{}
		idx.funcParams[fnID] = m
	}
	m[param] = true
}

func (idx *configPathPkgIndex) funcParamIsConfig(fnID, param string) bool {
	if idx == nil {
		return false
	}
	if idx.funcParams[fnID][param] {
		return true
	}
	if idx.adapter && looksLikeConfigPathName(param) {
		return true
	}
	return false
}

func buildConfigPathPkgIndex(files []*parsedOverlayFile) (*configPathPkgIndex, error) {
	idx := newConfigPathPkgIndex()
	if len(files) == 0 {
		return idx, nil
	}
	for _, pf := range files {
		if isConfigSourceAdapterPath(pf.filename) {
			idx.adapter = true
			break
		}
	}

	// Pass 1: package-level const/var string values and aliases.
	propagate := true
	for propagate {
		propagate = false
		for _, pf := range files {
			for _, decl := range pf.file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
					continue
				}
				for _, spec := range gen.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vs.Names {
						if name == nil || idx.pkgVals[name.Name] {
							continue
						}
						if i >= len(vs.Values) {
							continue
						}
						rhs := unwrapParenExpr(vs.Values[i])
						if looksLikeConfigPath(rhs) {
							idx.pkgVals[name.Name] = true
							propagate = true
							continue
						}
						if rid, ok := rhs.(*ast.Ident); ok && idx.pkgVals[rid.Name] {
							idx.pkgVals[name.Name] = true
							propagate = true
						}
					}
				}
			}
		}
	}

	// Pass 2: statement-ordered call-site / composite / field propagation.
	// Worklist fixpoint: each newly discovered param/field fact is recorded at
	// most once; the termination bound is derived from the finite fact space.
	factBound := configPathFactBound(files)
	for step := 0; ; step++ {
		if step > factBound {
			return nil, fmt.Errorf("config-path provenance fixpoint exceeded bound %d", factBound)
		}
		grew := false
		for _, pf := range files {
			for _, decl := range pf.file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil || fn.Name == nil {
					continue
				}
				fnID := callbackIdentity(fn, pf.pkgPath)
				// Seed adapter path-like params.
				if idx.adapter && fn.Type != nil && fn.Type.Params != nil {
					for _, field := range fn.Type.Params.List {
						for _, name := range field.Names {
							if name != nil && looksLikeConfigPathName(name.Name) {
								if !idx.funcParams[fnID][name.Name] {
									idx.markFuncParam(fnID, name.Name)
									grew = true
								}
							}
						}
					}
				}
				if indexConfigPathFuncOrdered(fn, idx) {
					grew = true
				}
			}
		}
		if !grew {
			break
		}
	}

	// Adapter: path-like struct fields are config by contract.
	if idx.adapter {
		for _, pf := range files {
			for _, decl := range pf.file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.TYPE {
					continue
				}
				for _, spec := range gen.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name == nil {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil {
						continue
					}
					for _, field := range st.Fields.List {
						for _, name := range field.Names {
							if name != nil && looksLikeConfigPathName(name.Name) {
								idx.markField(ts.Name.Name, name.Name)
							}
						}
					}
				}
			}
		}
	}
	return idx, nil
}

func indexConfigPathFuncOrdered(fn *ast.FuncDecl, idx *configPathPkgIndex) bool {
	if fn == nil || fn.Body == nil {
		return false
	}
	scope := seedPathScopeForFunc(fn, idx)
	grew := false
	var walkBlock func(block *ast.BlockStmt, scope *pathScope)
	var walkStmt func(stmt ast.Stmt, scope *pathScope)
	var walkExpr func(expr ast.Expr, scope *pathScope)

	walkExpr = func(expr ast.Expr, scope *pathScope) {
		if expr == nil {
			return
		}
		switch e := unwrapParenExpr(expr).(type) {
		case *ast.CallExpr:
			grew = indexConfigPathCallSite(e, fn, idx, scope) || grew
			walkExpr(e.Fun, scope)
			for _, arg := range e.Args {
				walkExpr(arg, scope)
			}
		case *ast.CompositeLit:
			grew = indexConfigPathComposite(e, fn, idx, scope) || grew
			for _, elt := range e.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					walkExpr(kv.Value, scope)
				} else {
					walkExpr(elt, scope)
				}
			}
		case *ast.UnaryExpr:
			if e.Op == token.AND {
				if cl, ok := unwrapParenExpr(e.X).(*ast.CompositeLit); ok {
					grew = indexConfigPathComposite(cl, fn, idx, scope) || grew
				}
			}
			walkExpr(e.X, scope)
		case *ast.BinaryExpr:
			walkExpr(e.X, scope)
			walkExpr(e.Y, scope)
		case *ast.SelectorExpr:
			walkExpr(e.X, scope)
		case *ast.IndexExpr:
			walkExpr(e.X, scope)
			walkExpr(e.Index, scope)
		case *ast.SliceExpr:
			walkExpr(e.X, scope)
			walkExpr(e.Low, scope)
			walkExpr(e.High, scope)
			walkExpr(e.Max, scope)
		case *ast.StarExpr:
			walkExpr(e.X, scope)
		case *ast.KeyValueExpr:
			walkExpr(e.Key, scope)
			walkExpr(e.Value, scope)
		case *ast.FuncLit:
			if e.Body != nil {
				child := newPathScope(scope)
				if e.Type != nil && e.Type.Params != nil {
					for _, field := range e.Type.Params.List {
						for _, name := range field.Names {
							if name != nil {
								child.declare(name.Name, false)
							}
						}
					}
				}
				walkBlock(e.Body, child)
			}
		}
	}

	indexRecvFieldAssign := func(assign *ast.AssignStmt, scope *pathScope) {
		for i, lhs := range assign.Lhs {
			if i >= len(assign.Rhs) {
				continue
			}
			sel, ok := unwrapParenExpr(lhs).(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				continue
			}
			rhs := unwrapParenExpr(assign.Rhs[i])
			if !exprHasConfigPathEvidence(rhs, idx, scope, fn, "") {
				continue
			}
			typeName := ""
			if recv := fn.Recv; recv != nil && len(recv.List) > 0 {
				recvName := ""
				if len(recv.List[0].Names) > 0 && recv.List[0].Names[0] != nil {
					recvName = recv.List[0].Names[0].Name
				}
				if id, ok := unwrapParenExpr(sel.X).(*ast.Ident); ok && id.Name == recvName {
					typeName = recvTypeLocalName(recv.List[0].Type)
				}
			}
			if typeName == "" {
				continue
			}
			if !idx.fields[typeName][sel.Sel.Name] {
				idx.markField(typeName, sel.Sel.Name)
				grew = true
			}
		}
	}

	walkStmt = func(stmt ast.Stmt, scope *pathScope) {
		if stmt == nil {
			return
		}
		switch s := stmt.(type) {
		case *ast.DeclStmt:
			if gen, ok := s.Decl.(*ast.GenDecl); ok {
				analyzePathGenDecl(gen, scope, idx, fn)
			}
		case *ast.AssignStmt:
			for _, rhs := range s.Rhs {
				walkExpr(rhs, scope)
			}
			indexRecvFieldAssign(s, scope)
			analyzePathAssign(s, scope, idx, fn)
		case *ast.ExprStmt:
			walkExpr(s.X, scope)
		case *ast.ReturnStmt:
			for _, r := range s.Results {
				walkExpr(r, scope)
			}
		case *ast.BlockStmt:
			child := newPathScope(scope)
			walkBlock(s, child)
		case *ast.IfStmt:
			ifScope := newPathScope(scope)
			if s.Init != nil {
				walkStmt(s.Init, ifScope)
			}
			walkExpr(s.Cond, ifScope)
			_, preexisting := forkPathEnv(ifScope)
			thenEnv, _ := forkPathEnv(ifScope)
			walkBlock(s.Body, thenEnv)
			var elseEnv *pathScope
			if s.Else == nil {
				elseEnv = snapshotPathEnv(ifScope, preexisting)
			} else {
				elseEnv, _ = forkPathEnv(ifScope)
				walkStmt(s.Else, elseEnv)
			}
			mergePathEnvs(ifScope, preexisting, thenEnv, elseEnv)
		case *ast.ForStmt:
			forScope := newPathScope(scope)
			if s.Init != nil {
				walkStmt(s.Init, forScope)
			}
			walkExpr(s.Cond, forScope)
			if !condStaticallyFalse(s.Cond) {
				bodyEnv, preexisting := forkPathEnv(forScope)
				walkBlock(s.Body, bodyEnv)
				if s.Post != nil {
					walkStmt(s.Post, bodyEnv)
				}
				// Merge only outer-visible names; construct-local := decls stay in forScope.
				outerPre := map[string]struct{}{}
				for name := range preexisting {
					if _, local := forScope.decls[name]; local {
						continue
					}
					outerPre[name] = struct{}{}
				}
				if len(outerPre) > 0 {
					branches := []*pathScope{snapshotPathEnv(bodyEnv, outerPre)}
					if !condStaticallyTrue(s.Cond) {
						branches = append(branches, snapshotPathEnv(forScope, outerPre))
					}
					mergePathEnvs(scope, outerPre, branches...)
				}
			}
		case *ast.RangeStmt:
			rangeScope := newPathScope(scope)
			walkExpr(s.X, scope)
			bodyEnv, preexisting := forkPathEnv(rangeScope)
			if s.Tok == token.DEFINE {
				if id, ok := s.Key.(*ast.Ident); ok {
					bodyEnv.declare(id.Name, false)
				}
				if id, ok := s.Value.(*ast.Ident); ok {
					bodyEnv.declare(id.Name, false)
				}
			}
			walkBlock(s.Body, bodyEnv)
			outerPre := map[string]struct{}{}
			for name := range preexisting {
				if _, local := rangeScope.decls[name]; local {
					continue
				}
				outerPre[name] = struct{}{}
			}
			if len(outerPre) > 0 {
				mergePathEnvs(scope, outerPre, bodyEnv, snapshotPathEnv(rangeScope, outerPre))
			}
		case *ast.SwitchStmt:
			swScope := newPathScope(scope)
			if s.Init != nil {
				walkStmt(s.Init, swScope)
			}
			walkExpr(s.Tag, swScope)
			_, preexisting := forkPathEnv(swScope)
			var branches []*pathScope
			hasDefault := false
			if s.Body != nil {
				for _, clause := range s.Body.List {
					cc, ok := clause.(*ast.CaseClause)
					if !ok {
						continue
					}
					if cc.List == nil {
						hasDefault = true
					}
					br, _ := forkPathEnv(swScope)
					for _, e := range cc.List {
						walkExpr(e, br)
					}
					walkBlock(&ast.BlockStmt{List: cc.Body}, br)
					branches = append(branches, br)
				}
			}
			if !hasDefault {
				branches = append(branches, snapshotPathEnv(swScope, preexisting))
			}
			if len(branches) > 0 {
				mergePathEnvs(swScope, preexisting, branches...)
			}
		case *ast.TypeSwitchStmt:
			swScope := newPathScope(scope)
			if s.Init != nil {
				walkStmt(s.Init, swScope)
			}
			if s.Assign != nil {
				walkStmt(s.Assign, swScope)
			}
			_, preexisting := forkPathEnv(swScope)
			var branches []*pathScope
			hasDefault := false
			if s.Body != nil {
				for _, clause := range s.Body.List {
					cc, ok := clause.(*ast.CaseClause)
					if !ok {
						continue
					}
					if cc.List == nil {
						hasDefault = true
					}
					br, _ := forkPathEnv(swScope)
					walkBlock(&ast.BlockStmt{List: cc.Body}, br)
					branches = append(branches, br)
				}
			}
			if !hasDefault {
				branches = append(branches, snapshotPathEnv(swScope, preexisting))
			}
			if len(branches) > 0 {
				mergePathEnvs(swScope, preexisting, branches...)
			}
		case *ast.GoStmt:
			walkExpr(s.Call, scope)
		case *ast.DeferStmt:
			walkExpr(s.Call, scope)
		case *ast.SendStmt:
			walkExpr(s.Chan, scope)
			walkExpr(s.Value, scope)
		case *ast.IncDecStmt:
			walkExpr(s.X, scope)
		}
	}

	walkBlock = func(block *ast.BlockStmt, scope *pathScope) {
		if block == nil {
			return
		}
		for _, stmt := range block.List {
			walkStmt(stmt, scope)
		}
	}

	walkBlock(fn.Body, scope)
	return grew
}

func indexConfigPathCallSite(call *ast.CallExpr, enclosing *ast.FuncDecl, idx *configPathPkgIndex, scope *pathScope) bool {
	grew := false
	fun := unwrapParenExpr(call.Fun)
	var targetID string
	switch f := fun.(type) {
	case *ast.Ident:
		targetID = f.Name
	default:
		return false
	}
	_ = enclosing
	for i, arg := range call.Args {
		if !exprHasConfigPathEvidence(arg, idx, scope, enclosing, "") {
			continue
		}
		name := positionalParamName(idx, targetID, i)
		if name == "" {
			name = fmt.Sprintf("#%d", i)
		}
		if !idx.funcParams[targetID][name] {
			idx.markFuncParam(targetID, name)
			grew = true
		}
	}
	return grew
}

func positionalParamName(idx *configPathPkgIndex, fnID string, i int) string {
	if idx == nil {
		return ""
	}
	// Names are filled when signatures are indexed; positional markers still propagate.
	key := fmt.Sprintf("#%d", i)
	if idx.funcParams[fnID][key] {
		return key
	}
	return key
}

func indexConfigPathComposite(lit *ast.CompositeLit, enclosing *ast.FuncDecl, idx *configPathPkgIndex, scope *pathScope) bool {
	if lit == nil {
		return false
	}
	typeName := ""
	switch t := unwrapParenExpr(lit.Type).(type) {
	case *ast.Ident:
		typeName = t.Name
	case *ast.SelectorExpr:
		if t.Sel != nil {
			typeName = t.Sel.Name
		}
	}
	if typeName == "" {
		return false
	}
	grew := false
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		if !exprHasConfigPathEvidence(kv.Value, idx, scope, enclosing, "") {
			continue
		}
		if !idx.fields[typeName][key.Name] {
			idx.markField(typeName, key.Name)
			grew = true
		}
	}
	return grew
}

func exprHasConfigPathEvidence(expr ast.Expr, idx *configPathPkgIndex, scope *pathScope, enclosing *ast.FuncDecl, recvType string) bool {
	expr = unwrapParenExpr(expr)
	if looksLikeConfigPath(expr) {
		return true
	}
	switch e := expr.(type) {
	case *ast.Ident:
		if scope != nil {
			if d := scope.lookup(e.Name); d != nil {
				return d.isConfig
			}
		}
		if idx != nil && idx.pkgVals[e.Name] {
			return true
		}
		if enclosing != nil && enclosing.Name != nil && idx != nil {
			fnID := callbackIdentity(enclosing, "")
			// Prefer short identity match for params (package-local names).
			if idx.funcParamIsConfig(enclosing.Name.Name, e.Name) ||
				idx.funcParamIsConfig(fnID, e.Name) {
				return true
			}
			// Positional: match parameter list order.
			if enclosing.Type != nil && enclosing.Type.Params != nil {
				pos := 0
				for _, field := range enclosing.Type.Params.List {
					for _, name := range field.Names {
						if name != nil && name.Name == e.Name {
							if idx.funcParamIsConfig(enclosing.Name.Name, fmt.Sprintf("#%d", pos)) ||
								idx.funcParamIsConfig(fnID, fmt.Sprintf("#%d", pos)) {
								return true
							}
						}
						pos++
					}
				}
			}
			if idx.adapter && looksLikeConfigPathName(e.Name) {
				// Parameter or local name in adapter.
				if enclosing.Type != nil && enclosing.Type.Params != nil {
					for _, field := range enclosing.Type.Params.List {
						for _, name := range field.Names {
							if name != nil && name.Name == e.Name {
								return true
							}
						}
					}
				}
			}
		}
		return false
	case *ast.SelectorExpr:
		if e.Sel == nil {
			return false
		}
		typeName := recvType
		if typeName == "" && enclosing != nil && enclosing.Recv != nil && len(enclosing.Recv.List) > 0 {
			recvName := ""
			if len(enclosing.Recv.List[0].Names) > 0 && enclosing.Recv.List[0].Names[0] != nil {
				recvName = enclosing.Recv.List[0].Names[0].Name
			}
			if id, ok := unwrapParenExpr(e.X).(*ast.Ident); ok && id.Name == recvName {
				typeName = recvTypeLocalName(enclosing.Recv.List[0].Type)
			}
		}
		if typeName != "" && idx != nil && idx.fieldIsConfig(typeName, e.Sel.Name) {
			return true
		}
		if idx != nil && idx.adapter && looksLikeConfigPathName(e.Sel.Name) {
			return true
		}
		return false
	default:
		return false
	}
}

func callHasConfigSourceEvidence(call *ast.CallExpr, filename string, aliasToPath map[string]string, aliases funcValueAliases, idx *configPathPkgIndex, scope *pathScope, enclosing *ast.FuncDecl) bool {
	pname, ok := osProbeCallName(call, aliasToPath, aliases)
	if !ok {
		return false
	}
	_ = pname
	if len(call.Args) == 0 {
		return isConfigSourceAdapterPath(filename)
	}
	if exprHasConfigPathEvidence(call.Args[0], idx, scope, enclosing, "") {
		return true
	}
	// Adapter contract: repeated probes of declared path params/fields count even
	// without a visible literal; one-shot allowance is enforced by control-flow.
	if isConfigSourceAdapterPath(filename) {
		arg := unwrapParenExpr(call.Args[0])
		switch a := arg.(type) {
		case *ast.Ident:
			if looksLikeConfigPathName(a.Name) {
				return true
			}
			// Any parameter of the enclosing adapter function.
			if enclosing != nil && enclosing.Type != nil && enclosing.Type.Params != nil {
				for _, field := range enclosing.Type.Params.List {
					for _, name := range field.Names {
						if name != nil && name.Name == a.Name {
							return true
						}
					}
				}
			}
		case *ast.SelectorExpr:
			if a.Sel != nil && looksLikeConfigPathName(a.Sel.Name) {
				return true
			}
		}
	}
	return false
}

func collectTimerValuesInFunc(fn *ast.FuncDecl, aliasToPath map[string]string, aliases funcValueAliases) map[string]bool {
	out := map[string]bool{}
	if fn.Body == nil {
		return out
	}
	mark := func(name string) {
		if name != "" && name != "_" {
			out[name] = true
		}
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) != len(n.Rhs) {
				return true
			}
			for i, lhs := range n.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				rhs := unwrapParenExpr(n.Rhs[i])
				if call, ok := rhs.(*ast.CallExpr); ok {
					name, ok := timeCallName(call, aliasToPath, aliases)
					if ok && isTimerValueCreateName(name) {
						mark(id.Name)
					}
					continue
				}
				if rid, ok := rhs.(*ast.Ident); ok && out[rid.Name] {
					mark(id.Name)
				}
			}
		case *ast.ValueSpec:
			for i, name := range n.Names {
				if i >= len(n.Values) {
					continue
				}
				call, ok := unwrapParenExpr(n.Values[i]).(*ast.CallExpr)
				if !ok {
					continue
				}
				tname, ok := timeCallName(call, aliasToPath, aliases)
				if ok && isTimerValueCreateName(tname) {
					mark(name.Name)
				}
			}
		}
		return true
	})
	return out
}

func collectTimerChannelsInFunc(fn *ast.FuncDecl, timerVals map[string]bool, aliasToPath map[string]string, aliases funcValueAliases) map[string]bool {
	out := map[string]bool{}
	if fn.Body == nil {
		return out
	}
	mark := func(name string) {
		if name != "" && name != "_" {
			out[name] = true
		}
	}
	propagate := true
	for propagate {
		propagate = false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				if len(n.Lhs) != len(n.Rhs) {
					return true
				}
				for i, lhs := range n.Lhs {
					id, ok := lhs.(*ast.Ident)
					if !ok || out[id.Name] {
						continue
					}
					rhs := unwrapParenExpr(n.Rhs[i])
					if call, ok := rhs.(*ast.CallExpr); ok {
						tname, ok := timeCallName(call, aliasToPath, aliases)
						if ok && isTimerChannelCreateName(tname) {
							mark(id.Name)
							propagate = true
						}
						continue
					}
					if sel, ok := rhs.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == "C" {
						if tid, ok := unwrapParenExpr(sel.X).(*ast.Ident); ok && timerVals[tid.Name] {
							mark(id.Name)
							propagate = true
						}
						continue
					}
					if rid, ok := rhs.(*ast.Ident); ok && out[rid.Name] {
						mark(id.Name)
						propagate = true
					}
				}
			case *ast.ValueSpec:
				for i, name := range n.Names {
					if name == nil || out[name.Name] || i >= len(n.Values) {
						continue
					}
					rhs := unwrapParenExpr(n.Values[i])
					if call, ok := rhs.(*ast.CallExpr); ok {
						tname, ok := timeCallName(call, aliasToPath, aliases)
						if ok && isTimerChannelCreateName(tname) {
							mark(name.Name)
							propagate = true
						}
						continue
					}
					if sel, ok := rhs.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == "C" {
						if tid, ok := unwrapParenExpr(sel.X).(*ast.Ident); ok && timerVals[tid.Name] {
							mark(name.Name)
							propagate = true
						}
					} else if rid, ok := rhs.(*ast.Ident); ok && out[rid.Name] {
						mark(name.Name)
						propagate = true
					}
				}
			}
			return true
		})
	}
	return out
}

func isTimerChannelExpr(expr ast.Expr, timerVals, timerChans map[string]bool, aliasToPath map[string]string, aliases funcValueAliases) bool {
	expr = unwrapParenExpr(expr)
	if id, ok := expr.(*ast.Ident); ok {
		return timerChans[id.Name]
	}
	if call, ok := expr.(*ast.CallExpr); ok {
		tname, ok := timeCallName(call, aliasToPath, aliases)
		return ok && isTimerChannelCreateName(tname)
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "C" {
		return false
	}
	id, ok := unwrapParenExpr(sel.X).(*ast.Ident)
	return ok && timerVals[id.Name]
}

// loopIsRepeatable reports whether control-flow may execute the body more than
// once. Finite only when control-flow proves at most one iteration.
func loopIsRepeatable(loop ast.Node, timerVals, timerChans map[string]bool, aliasToPath map[string]string, aliases funcValueAliases) bool {
	switch l := loop.(type) {
	case *ast.ForStmt:
		if provesAtMostOneIteration(l) {
			return false
		}
		return true
	case *ast.RangeStmt:
		return isTimerChannelExpr(l.X, timerVals, timerChans, aliasToPath, aliases)
	default:
		return false
	}
}

func condStaticallyFalse(cond ast.Expr) bool {
	if cond == nil {
		return false
	}
	id, ok := unwrapParenExpr(cond).(*ast.Ident)
	return ok && id.Name == "false"
}

func condStaticallyTrue(cond ast.Expr) bool {
	if cond == nil {
		return true // for { } / for ;;
	}
	id, ok := unwrapParenExpr(cond).(*ast.Ident)
	return ok && id.Name == "true"
}

// indexLabelsIn maps label names to their LabeledStmt nodes under root.
func indexLabelsIn(root ast.Node) map[string]*ast.LabeledStmt {
	out := map[string]*ast.LabeledStmt{}
	if root == nil {
		return out
	}
	ast.Inspect(root, func(n ast.Node) bool {
		ls, ok := n.(*ast.LabeledStmt)
		if !ok || ls.Label == nil || ls.Label.Name == "" {
			return true
		}
		if _, exists := out[ls.Label.Name]; !exists {
			out[ls.Label.Name] = ls
		}
		return true
	})
	return out
}

func labeledStmtInside(target *ast.LabeledStmt, container ast.Node) bool {
	if target == nil || container == nil {
		return false
	}
	found := false
	ast.Inspect(container, func(n ast.Node) bool {
		if n == target {
			found = true
			return false
		}
		return !found
	})
	return found
}

// peelLabeledStmt unwraps stacked labels, returning label names and the core stmt.
func peelLabeledStmt(stmt ast.Stmt) (labels []string, core ast.Stmt) {
	core = stmt
	for {
		ls, ok := core.(*ast.LabeledStmt)
		if !ok {
			return labels, core
		}
		if ls.Label != nil && ls.Label.Name != "" {
			labels = append(labels, ls.Label.Name)
		}
		core = ls.Stmt
	}
}

// labelPathFrame is one lexical frame on the path from a statement-list root to a label.
type labelPathFrame struct {
	list  []ast.Stmt
	index int
}

// labelSite is a function-local label with its lexical AST path from an indexing root.
type labelSite struct {
	labeled *ast.LabeledStmt
	path    []labelPathFrame
}

// indexLabelsWithPath recursively indexes labels under stmts with lexical paths.
func indexLabelsWithPath(stmts []ast.Stmt) map[string]labelSite {
	out := map[string]labelSite{}
	var walk func([]ast.Stmt, []labelPathFrame)
	walk = func(list []ast.Stmt, prefix []labelPathFrame) {
		for i, st := range list {
			if st == nil {
				continue
			}
			path := append(append([]labelPathFrame{}, prefix...), labelPathFrame{list: list, index: i})
			cur := st
			for {
				ls, ok := cur.(*ast.LabeledStmt)
				if !ok {
					break
				}
				if ls.Label != nil && ls.Label.Name != "" {
					if _, exists := out[ls.Label.Name]; !exists {
						out[ls.Label.Name] = labelSite{labeled: ls, path: path}
					}
				}
				cur = ls.Stmt
			}
			_, core := peelLabeledStmt(st)
			switch s := core.(type) {
			case *ast.BlockStmt:
				if s != nil {
					walk(s.List, path)
				}
			case *ast.IfStmt:
				if s.Body != nil {
					walk(s.Body.List, path)
				}
				if s.Else != nil {
					if eb, ok := s.Else.(*ast.BlockStmt); ok {
						walk(eb.List, path)
					} else {
						walk([]ast.Stmt{s.Else}, path)
					}
				}
			case *ast.ForStmt:
				if s.Body != nil {
					walk(s.Body.List, path)
				}
			case *ast.RangeStmt:
				if s.Body != nil {
					walk(s.Body.List, path)
				}
			case *ast.SwitchStmt:
				if s.Body != nil {
					for _, clause := range s.Body.List {
						if cc, ok := clause.(*ast.CaseClause); ok {
							walk(cc.Body, path)
						}
					}
				}
			case *ast.TypeSwitchStmt:
				if s.Body != nil {
					for _, clause := range s.Body.List {
						if cc, ok := clause.(*ast.CaseClause); ok {
							walk(cc.Body, path)
						}
					}
				}
			case *ast.SelectStmt:
				if s.Body != nil {
					for _, clause := range s.Body.List {
						if cc, ok := clause.(*ast.CommClause); ok {
							walk(cc.Body, path)
						}
					}
				}
			}
		}
	}
	walk(stmts, nil)
	return out
}

// ctrlTargetKind classifies a breakable / continuable construct.
type ctrlTargetKind int

const (
	ctrlKindFor ctrlTargetKind = iota
	ctrlKindRange
	ctrlKindSwitch
	ctrlKindTypeSwitch
	ctrlKindSelect
)

const analyzedLoopTargetID = 0

// ctrlTarget is one lexical control-flow target (loop / switch / select).
type ctrlTarget struct {
	id     int
	kind   ctrlTargetKind
	labels map[string]bool
}

func (t *ctrlTarget) isLoop() bool {
	return t != nil && (t.kind == ctrlKindFor || t.kind == ctrlKindRange)
}

func (t *ctrlTarget) isBreakFallthrough() bool {
	return t != nil && (t.kind == ctrlKindSwitch || t.kind == ctrlKindTypeSwitch || t.kind == ctrlKindSelect)
}

// nameShadowScope tracks lexical identifier declarations for built-in panic vs local shadow resolution.
type nameShadowScope struct {
	parent *nameShadowScope
	names  map[string]bool
}

func newNameShadowScope(parent *nameShadowScope) *nameShadowScope {
	return &nameShadowScope{parent: parent, names: map[string]bool{}}
}

func (s *nameShadowScope) declare(name string) {
	if s == nil || name == "" || name == "_" {
		return
	}
	s.names[name] = true
}

func (s *nameShadowScope) shadows(name string) bool {
	for cur := s; cur != nil; cur = cur.parent {
		if cur.names[name] {
			return true
		}
	}
	return false
}

func declareFieldListShadowNames(scope *nameShadowScope, fields *ast.FieldList) {
	if scope == nil || fields == nil {
		return
	}
	for _, f := range fields.List {
		for _, n := range f.Names {
			if n != nil {
				scope.declare(n.Name)
			}
		}
	}
}

func declareGenDeclShadowNames(scope *nameShadowScope, gen *ast.GenDecl) {
	if scope == nil || gen == nil {
		return
	}
	for _, spec := range gen.Specs {
		switch sp := spec.(type) {
		case *ast.ValueSpec:
			for _, n := range sp.Names {
				if n != nil {
					scope.declare(n.Name)
				}
			}
		case *ast.TypeSpec:
			if sp.Name != nil {
				scope.declare(sp.Name.Name)
			}
		}
	}
}

func declareAssignDefineShadowNames(scope *nameShadowScope, assign *ast.AssignStmt) {
	if scope == nil || assign == nil || assign.Tok != token.DEFINE {
		return
	}
	for _, lhs := range assign.Lhs {
		if id, ok := unwrapParenExpr(lhs).(*ast.Ident); ok {
			scope.declare(id.Name)
		}
	}
}

// seedNameShadowsAtLoop builds the lexical name scope visible at loopBody.
// Package-scope names are seeded first, then function params/results/receivers,
// then declarations along the enclosing path (blocks, if/for/switch init, FuncLit
// params). Shadows that end before loopBody do not leak.
func seedNameShadowsAtLoop(fn *ast.FuncDecl, loopBody *ast.BlockStmt, pkgShadows map[string]bool) *nameShadowScope {
	scope := newNameShadowScope(nil)
	if pkgShadows != nil {
		names := make([]string, 0, len(pkgShadows))
		for name := range pkgShadows {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			scope.declare(name)
		}
	}
	if fn == nil {
		return scope
	}
	declareFieldListShadowNames(scope, fn.Recv)
	if fn.Type != nil {
		declareFieldListShadowNames(scope, fn.Type.Params)
		declareFieldListShadowNames(scope, fn.Type.Results)
	}
	if fn.Body == nil || loopBody == nil {
		return scope
	}
	return seedNameShadowsInStmts(scope, fn.Body.List, loopBody)
}

func seedNameShadowsInStmts(scope *nameShadowScope, stmts []ast.Stmt, target ast.Node) *nameShadowScope {
	if scope == nil {
		scope = newNameShadowScope(nil)
	}
	for _, st := range stmts {
		if st == nil {
			continue
		}
		if st == target {
			return scope
		}
		if !stmtContainsNode(st, target) {
			declareStmtShadowNames(scope, st)
			continue
		}
		return seedNameShadowsIntoContainingStmt(scope, st, target)
	}
	return scope
}

func seedNameShadowsIntoContainingStmt(scope *nameShadowScope, stmt ast.Stmt, target ast.Node) *nameShadowScope {
	if scope == nil {
		scope = newNameShadowScope(nil)
	}
	if stmt == nil {
		return scope
	}
	_, core := peelLabeledStmt(stmt)
	if core == target {
		return scope
	}
	switch s := core.(type) {
	case *ast.BlockStmt:
		child := newNameShadowScope(scope)
		if s == target {
			return child
		}
		return seedNameShadowsInStmts(child, s.List, target)
	case *ast.IfStmt:
		child := newNameShadowScope(scope)
		if s.Init != nil {
			if s.Init == target || stmtContainsNode(s.Init, target) {
				return seedNameShadowsIntoContainingStmt(child, s.Init, target)
			}
			declareStmtShadowNames(child, s.Init)
		}
		if s.Body != nil && (s.Body == target || stmtContainsNode(s.Body, target)) {
			bodyScope := newNameShadowScope(child)
			if s.Body == target {
				return bodyScope
			}
			return seedNameShadowsInStmts(bodyScope, s.Body.List, target)
		}
		if s.Else != nil && (s.Else == target || stmtContainsNode(s.Else, target)) {
			elseScope := newNameShadowScope(child)
			return seedNameShadowsIntoContainingStmt(elseScope, s.Else, target)
		}
		return child
	case *ast.ForStmt:
		child := newNameShadowScope(scope)
		if s.Init != nil {
			if s.Init == target || stmtContainsNode(s.Init, target) {
				return seedNameShadowsIntoContainingStmt(child, s.Init, target)
			}
			declareStmtShadowNames(child, s.Init)
		}
		if s.Body == target {
			return child
		}
		if s.Body != nil && stmtContainsNode(s.Body, target) {
			return seedNameShadowsInStmts(child, s.Body.List, target)
		}
		return child
	case *ast.RangeStmt:
		child := newNameShadowScope(scope)
		if s.Tok == token.DEFINE {
			if id, ok := unwrapParenExpr(s.Key).(*ast.Ident); ok {
				child.declare(id.Name)
			}
			if id, ok := unwrapParenExpr(s.Value).(*ast.Ident); ok {
				child.declare(id.Name)
			}
		}
		if s.Body == target {
			return child
		}
		if s.Body != nil && stmtContainsNode(s.Body, target) {
			return seedNameShadowsInStmts(child, s.Body.List, target)
		}
		return child
	case *ast.SwitchStmt:
		child := newNameShadowScope(scope)
		if s.Init != nil {
			if s.Init == target || stmtContainsNode(s.Init, target) {
				return seedNameShadowsIntoContainingStmt(child, s.Init, target)
			}
			declareStmtShadowNames(child, s.Init)
		}
		if s.Body != nil && stmtContainsNode(s.Body, target) {
			return seedNameShadowsInStmts(child, s.Body.List, target)
		}
		return child
	case *ast.TypeSwitchStmt:
		child := newNameShadowScope(scope)
		if s.Init != nil {
			if s.Init == target || stmtContainsNode(s.Init, target) {
				return seedNameShadowsIntoContainingStmt(child, s.Init, target)
			}
			declareStmtShadowNames(child, s.Init)
		}
		if s.Assign != nil {
			if s.Assign == target || stmtContainsNode(s.Assign, target) {
				return seedNameShadowsIntoContainingStmt(child, s.Assign, target)
			}
			declareStmtShadowNames(child, s.Assign)
		}
		if s.Body != nil && stmtContainsNode(s.Body, target) {
			return seedNameShadowsInStmts(child, s.Body.List, target)
		}
		return child
	case *ast.SelectStmt:
		if s.Body != nil && stmtContainsNode(s.Body, target) {
			return seedNameShadowsInStmts(newNameShadowScope(scope), s.Body.List, target)
		}
		return scope
	case *ast.CaseClause:
		return seedNameShadowsInStmts(newNameShadowScope(scope), s.Body, target)
	case *ast.CommClause:
		child := newNameShadowScope(scope)
		if s.Comm != nil {
			if s.Comm == target || stmtContainsNode(s.Comm, target) {
				return seedNameShadowsIntoContainingStmt(child, s.Comm, target)
			}
			declareStmtShadowNames(child, s.Comm)
		}
		return seedNameShadowsInStmts(child, s.Body, target)
	default:
		return seedNameShadowsInExprNode(scope, core, target)
	}
}

func seedNameShadowsInExprNode(scope *nameShadowScope, node ast.Node, target ast.Node) *nameShadowScope {
	if scope == nil {
		scope = newNameShadowScope(nil)
	}
	if node == nil {
		return scope
	}
	var result *nameShadowScope
	ast.Inspect(node, func(n ast.Node) bool {
		if result != nil {
			return false
		}
		lit, ok := n.(*ast.FuncLit)
		if !ok || lit.Body == nil {
			return true
		}
		if lit.Body != target && !stmtContainsNode(lit.Body, target) {
			return true
		}
		child := newNameShadowScope(scope)
		if lit.Type != nil {
			declareFieldListShadowNames(child, lit.Type.Params)
			declareFieldListShadowNames(child, lit.Type.Results)
		}
		if lit.Body == target {
			result = child
			return false
		}
		result = seedNameShadowsInStmts(child, lit.Body.List, target)
		return false
	})
	if result != nil {
		return result
	}
	return scope
}

func declareStmtShadowNames(scope *nameShadowScope, stmt ast.Stmt) {
	if scope == nil || stmt == nil {
		return
	}
	_, core := peelLabeledStmt(stmt)
	switch s := core.(type) {
	case *ast.DeclStmt:
		if gen, ok := s.Decl.(*ast.GenDecl); ok {
			declareGenDeclShadowNames(scope, gen)
		}
	case *ast.AssignStmt:
		declareAssignDefineShadowNames(scope, s)
	case *ast.BlockStmt:
		child := newNameShadowScope(scope)
		for _, st := range s.List {
			declareStmtShadowNames(child, st)
		}
		// Block ended — names do not leak; keep parent scope.
	}
}

// isBuiltinPanicCall reports a call to the predeclared panic built-in.
// Only an unqualified identifier that resolves to the universe built-in is
// terminal. Package-level or lexical shadows, and selector calls, are not.
// When a legal shadow cannot be ruled out, returns false (conservative).
func isBuiltinPanicCall(expr ast.Expr, lex *nameShadowScope) bool {
	call, ok := unwrapParenExpr(expr).(*ast.CallExpr)
	if !ok {
		return false
	}
	// Selectors (pkg.panic, obj.panic) are never the predeclared built-in.
	if _, ok := unwrapParenExpr(call.Fun).(*ast.SelectorExpr); ok {
		return false
	}
	id, ok := unwrapParenExpr(call.Fun).(*ast.Ident)
	if !ok || id.Name != "panic" {
		return false
	}
	if lex == nil {
		// Uncertain lexical/package resolution — do not claim terminality.
		return false
	}
	if lex.shadows("panic") {
		return false
	}
	return true
}

func provesAtMostOneIteration(fs *ast.ForStmt) bool {
	if fs.Body == nil || condStaticallyFalse(fs.Cond) {
		return true
	}
	// A reachable backward goto can revisit the body before the for-post runs,
	// invalidating any at-most-one induction proof.
	if bodyHasBackwardGoto(fs.Body) {
		return false
	}
	if provesInductionAtMostOne(fs) {
		return true
	}
	return blockExitsBeforeBackedge(fs.Body)
}

// bodyHasBackwardGoto reports a goto whose label appears at or before the goto
// in statement order within body (a backedge / self-cycle). Forward gotos that
// cannot cycle do not invalidate one-iteration proofs. Unknown labels fail safe.
func bodyHasBackwardGoto(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	order := 0
	labelAt := map[string]int{}
	type gotoRef struct {
		label string
		at    int
	}
	var gotos []gotoRef

	var walkStmts func([]ast.Stmt)
	var walkStmt func(ast.Stmt)
	walkStmt = func(stmt ast.Stmt) {
		if stmt == nil {
			return
		}
		order++
		at := order
		switch s := stmt.(type) {
		case *ast.LabeledStmt:
			if s.Label != nil {
				labelAt[s.Label.Name] = at
			}
			walkStmt(s.Stmt)
		case *ast.BranchStmt:
			if s.Tok == token.GOTO && s.Label != nil {
				gotos = append(gotos, gotoRef{label: s.Label.Name, at: at})
			}
		case *ast.BlockStmt:
			walkStmts(s.List)
		case *ast.IfStmt:
			walkStmt(s.Init)
			if s.Body != nil {
				walkStmts(s.Body.List)
			}
			walkStmt(s.Else)
		case *ast.ForStmt:
			walkStmt(s.Init)
			if s.Body != nil {
				walkStmts(s.Body.List)
			}
			walkStmt(s.Post)
		case *ast.RangeStmt:
			if s.Body != nil {
				walkStmts(s.Body.List)
			}
		case *ast.SwitchStmt:
			walkStmt(s.Init)
			if s.Body != nil {
				walkStmts(s.Body.List)
			}
		case *ast.TypeSwitchStmt:
			walkStmt(s.Init)
			walkStmt(s.Assign)
			if s.Body != nil {
				walkStmts(s.Body.List)
			}
		case *ast.SelectStmt:
			if s.Body != nil {
				walkStmts(s.Body.List)
			}
		case *ast.CaseClause:
			walkStmts(s.Body)
		case *ast.CommClause:
			walkStmt(s.Comm)
			walkStmts(s.Body)
		}
	}
	walkStmts = func(stmts []ast.Stmt) {
		for _, st := range stmts {
			walkStmt(st)
		}
	}
	walkStmts(body.List)
	for _, g := range gotos {
		at, ok := labelAt[g.label]
		if !ok || at <= g.at {
			return true
		}
	}
	return false
}

// provesInductionAtMostOne proves the classic counted for-loop executes the body
// at most once: known init literal, understood condition orientation, known
// update magnitude/direction on every backedge, and after one update the
// condition is false. Unknown facts → not proven (treat as potentially repeated).
func provesInductionAtMostOne(fs *ast.ForStmt) bool {
	name, initVal, ok := forInitLiteralBinding(fs.Init)
	if !ok || name == "" {
		return false
	}
	op, bound, ok := condCompareLiteral(fs.Cond, name)
	if !ok {
		return false
	}
	if !evalIntCompare(initVal, op, bound) {
		// Zero iterations is still at most one.
		return true
	}
	delta, fromPost, ok := inductionUpdateDelta(fs, name)
	if !ok {
		return false
	}
	if inductionBodyInterferes(fs, name, fromPost) {
		return false
	}
	next := initVal + delta
	return !evalIntCompare(next, op, bound)
}

func forInitLiteralBinding(init ast.Stmt) (name string, val int, ok bool) {
	if init == nil {
		return "", 0, false
	}
	assign, ok := init.(*ast.AssignStmt)
	if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return "", 0, false
	}
	id, ok := unwrapParenExpr(assign.Lhs[0]).(*ast.Ident)
	if !ok || id.Name == "" || id.Name == "_" {
		return "", 0, false
	}
	val, ok = intLiteralValue(assign.Rhs[0])
	if !ok {
		return "", 0, false
	}
	return id.Name, val, true
}

func intLiteralValue(expr ast.Expr) (int, bool) {
	lit, ok := unwrapParenExpr(expr).(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, false
	}
	n := 0
	for _, ch := range lit.Value {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
	}
	return n, true
}

func condCompareLiteral(cond ast.Expr, wantName string) (op token.Token, bound int, ok bool) {
	cond = unwrapParenExpr(cond)
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok {
		return 0, 0, false
	}
	switch bin.Op {
	case token.LSS, token.LEQ, token.GTR, token.GEQ, token.EQL, token.NEQ:
	default:
		return 0, 0, false
	}
	if id, isID := unwrapParenExpr(bin.X).(*ast.Ident); isID && id.Name == wantName {
		bound, ok = intLiteralValue(bin.Y)
		if !ok {
			return 0, 0, false
		}
		return bin.Op, bound, true
	}
	if id, isID := unwrapParenExpr(bin.Y).(*ast.Ident); isID && id.Name == wantName {
		bound, ok = intLiteralValue(bin.X)
		if !ok {
			return 0, 0, false
		}
		return flipCompareOp(bin.Op), bound, true
	}
	return 0, 0, false
}

func flipCompareOp(op token.Token) token.Token {
	switch op {
	case token.LSS:
		return token.GTR
	case token.LEQ:
		return token.GEQ
	case token.GTR:
		return token.LSS
	case token.GEQ:
		return token.LEQ
	case token.EQL, token.NEQ:
		return op
	default:
		return op
	}
}

func evalIntCompare(val int, op token.Token, bound int) bool {
	switch op {
	case token.LSS:
		return val < bound
	case token.LEQ:
		return val <= bound
	case token.GTR:
		return val > bound
	case token.GEQ:
		return val >= bound
	case token.EQL:
		return val == bound
	case token.NEQ:
		return val != bound
	default:
		return false
	}
}

// inductionUpdateDelta returns the known numeric delta applied to name on every
// backedge, and whether that update lives in the for-post statement.
func inductionUpdateDelta(fs *ast.ForStmt, name string) (delta int, fromPost bool, ok bool) {
	if name == "" {
		return 0, false, false
	}
	if d, ok := stmtInductionDelta(fs.Post, name); ok {
		return d, true, true
	}
	if d, ok := blockUnconditionalInductionDelta(fs.Body, name); ok {
		return d, false, true
	}
	return 0, false, false
}

func stmtInductionDelta(stmt ast.Stmt, name string) (int, bool) {
	if stmt == nil || name == "" {
		return 0, false
	}
	switch s := stmt.(type) {
	case *ast.IncDecStmt:
		id, ok := unwrapParenExpr(s.X).(*ast.Ident)
		if !ok || id.Name != name {
			return 0, false
		}
		switch s.Tok {
		case token.INC:
			return 1, true
		case token.DEC:
			return -1, true
		}
	case *ast.AssignStmt:
		if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
			return 0, false
		}
		id, ok := unwrapParenExpr(s.Lhs[0]).(*ast.Ident)
		if !ok || id.Name != name {
			return 0, false
		}
		switch s.Tok {
		case token.ADD_ASSIGN:
			return intLiteralValue(s.Rhs[0])
		case token.SUB_ASSIGN:
			n, ok := intLiteralValue(s.Rhs[0])
			return -n, ok
		case token.ASSIGN:
			rhs := unwrapParenExpr(s.Rhs[0])
			bin, ok := rhs.(*ast.BinaryExpr)
			if !ok {
				return 0, false
			}
			lid, lok := unwrapParenExpr(bin.X).(*ast.Ident)
			switch bin.Op {
			case token.ADD:
				if lok && lid.Name == name {
					return intLiteralValue(bin.Y)
				}
				rid, rok := unwrapParenExpr(bin.Y).(*ast.Ident)
				if rok && rid.Name == name {
					return intLiteralValue(bin.X)
				}
			case token.SUB:
				if lok && lid.Name == name {
					n, ok := intLiteralValue(bin.Y)
					return -n, ok
				}
			}
		}
	}
	return 0, false
}

func blockUnconditionalInductionDelta(body *ast.BlockStmt, name string) (int, bool) {
	if body == nil {
		return 0, false
	}
	for _, st := range body.List {
		switch s := st.(type) {
		case *ast.BranchStmt:
			if s.Tok == token.CONTINUE {
				return 0, false
			}
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
			return 0, false
		default:
			if d, ok := stmtInductionDelta(s, name); ok {
				return d, true
			}
			if blk, ok := s.(*ast.BlockStmt); ok {
				if d, ok := blockUnconditionalInductionDelta(blk, name); ok {
					return d, true
				}
			}
		}
	}
	return 0, false
}

// inductionBodyInterferes reports body writes/continues that defeat a one-shot
// induction proof. Writes are resolved by lexical declaration identity so inner
// shadows do not invalidate the outer induction variable.
func inductionBodyInterferes(fs *ast.ForStmt, name string, advanceFromPost bool) bool {
	if fs.Body == nil || name == "" {
		return false
	}
	idGen := 0
	root := newLexNameScope(nil)
	indID := root.declare(name, &idGen)
	if advanceFromPost {
		return blockWritesLexIdent(fs.Body, name, indID, root, &idGen)
	}
	seenAdvance := false
	for _, st := range fs.Body.List {
		switch s := st.(type) {
		case *ast.BranchStmt:
			if s.Tok == token.CONTINUE && !seenAdvance {
				return true
			}
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
			if stmtWritesLexIdent(s, name, indID, root, &idGen) {
				return true
			}
		default:
			if stmtAdvancesLexIdent(s, name, indID, root, &idGen) && !seenAdvance {
				seenAdvance = true
				continue
			}
			if stmtWritesLexIdent(s, name, indID, root, &idGen) {
				return true
			}
		}
	}
	return false
}

// lexNameScope tracks declaration identity by spelling within nested scopes.
type lexNameScope struct {
	parent *lexNameScope
	ids    map[string]int
}

func newLexNameScope(parent *lexNameScope) *lexNameScope {
	return &lexNameScope{parent: parent, ids: map[string]int{}}
}

func (s *lexNameScope) declare(name string, idGen *int) int {
	if s == nil || name == "" || name == "_" || idGen == nil {
		return 0
	}
	*idGen++
	id := *idGen
	s.ids[name] = id
	return id
}

func (s *lexNameScope) lookup(name string) (int, bool) {
	for cur := s; cur != nil; cur = cur.parent {
		if id, ok := cur.ids[name]; ok {
			return id, true
		}
	}
	return 0, false
}

func blockWritesLexIdent(body *ast.BlockStmt, name string, wantID int, scope *lexNameScope, idGen *int) bool {
	if body == nil {
		return false
	}
	child := newLexNameScope(scope)
	for _, st := range body.List {
		if stmtWritesLexIdent(st, name, wantID, child, idGen) {
			return true
		}
	}
	return false
}

func stmtWritesLexIdent(stmt ast.Stmt, name string, wantID int, scope *lexNameScope, idGen *int) bool {
	if stmt == nil || name == "" || wantID == 0 {
		return false
	}
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return blockWritesLexIdent(s, name, wantID, scope, idGen)
	case *ast.IncDecStmt:
		return exprWritesLexIdent(s.X, name, wantID, scope)
	case *ast.AssignStmt:
		if s.Tok == token.DEFINE {
			for _, lhs := range s.Lhs {
				if id, ok := unwrapParenExpr(lhs).(*ast.Ident); ok {
					scope.declare(id.Name, idGen)
				}
			}
			return false
		}
		for _, lhs := range s.Lhs {
			if exprWritesLexIdent(lhs, name, wantID, scope) {
				return true
			}
		}
		return false
	case *ast.DeclStmt:
		if gen, ok := s.Decl.(*ast.GenDecl); ok {
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, n := range vs.Names {
					if n != nil {
						scope.declare(n.Name, idGen)
					}
				}
			}
		}
		return false
	case *ast.IfStmt:
		ifScope := newLexNameScope(scope)
		if s.Init != nil && stmtWritesLexIdent(s.Init, name, wantID, ifScope, idGen) {
			return true
		}
		if s.Body != nil && blockWritesLexIdent(s.Body, name, wantID, ifScope, idGen) {
			return true
		}
		if s.Else != nil && stmtWritesLexIdent(s.Else, name, wantID, ifScope, idGen) {
			return true
		}
		return false
	case *ast.ForStmt:
		forScope := newLexNameScope(scope)
		if s.Init != nil && stmtWritesLexIdent(s.Init, name, wantID, forScope, idGen) {
			return true
		}
		if s.Body != nil && blockWritesLexIdent(s.Body, name, wantID, forScope, idGen) {
			return true
		}
		if s.Post != nil && stmtWritesLexIdent(s.Post, name, wantID, forScope, idGen) {
			return true
		}
		return false
	case *ast.RangeStmt:
		rangeScope := newLexNameScope(scope)
		if s.Tok == token.DEFINE {
			if id, ok := s.Key.(*ast.Ident); ok {
				rangeScope.declare(id.Name, idGen)
			}
			if id, ok := s.Value.(*ast.Ident); ok {
				rangeScope.declare(id.Name, idGen)
			}
		} else {
			if exprWritesLexIdent(s.Key, name, wantID, rangeScope) {
				return true
			}
			if exprWritesLexIdent(s.Value, name, wantID, rangeScope) {
				return true
			}
		}
		return s.Body != nil && blockWritesLexIdent(s.Body, name, wantID, rangeScope, idGen)
	case *ast.SwitchStmt:
		sw := newLexNameScope(scope)
		if s.Init != nil && stmtWritesLexIdent(s.Init, name, wantID, sw, idGen) {
			return true
		}
		if s.Body != nil {
			for _, clause := range s.Body.List {
				if stmtWritesLexIdent(clause, name, wantID, sw, idGen) {
					return true
				}
			}
		}
		return false
	case *ast.TypeSwitchStmt:
		sw := newLexNameScope(scope)
		if s.Init != nil && stmtWritesLexIdent(s.Init, name, wantID, sw, idGen) {
			return true
		}
		if s.Assign != nil && stmtWritesLexIdent(s.Assign, name, wantID, sw, idGen) {
			return true
		}
		if s.Body != nil {
			for _, clause := range s.Body.List {
				if stmtWritesLexIdent(clause, name, wantID, sw, idGen) {
					return true
				}
			}
		}
		return false
	case *ast.SelectStmt:
		if s.Body == nil {
			return false
		}
		for _, clause := range s.Body.List {
			if stmtWritesLexIdent(clause, name, wantID, scope, idGen) {
				return true
			}
		}
		return false
	case *ast.CaseClause:
		child := newLexNameScope(scope)
		for _, cs := range s.Body {
			if stmtWritesLexIdent(cs, name, wantID, child, idGen) {
				return true
			}
		}
		return false
	case *ast.CommClause:
		child := newLexNameScope(scope)
		if s.Comm != nil && stmtWritesLexIdent(s.Comm, name, wantID, child, idGen) {
			return true
		}
		for _, cs := range s.Body {
			if stmtWritesLexIdent(cs, name, wantID, child, idGen) {
				return true
			}
		}
		return false
	case *ast.LabeledStmt:
		return stmtWritesLexIdent(s.Stmt, name, wantID, scope, idGen)
	case *ast.GoStmt:
		if s.Call != nil {
			return exprWritesLexIdentInFuncLits(s.Call, name, wantID, scope, idGen)
		}
		return false
	case *ast.DeferStmt:
		if s.Call != nil {
			return exprWritesLexIdentInFuncLits(s.Call, name, wantID, scope, idGen)
		}
		return false
	case *ast.ExprStmt:
		return exprWritesLexIdentInFuncLits(s.X, name, wantID, scope, idGen)
	default:
		return false
	}
}

func exprWritesLexIdent(expr ast.Expr, name string, wantID int, scope *lexNameScope) bool {
	id, ok := unwrapParenExpr(expr).(*ast.Ident)
	if !ok || id.Name != name {
		return false
	}
	got, found := scope.lookup(name)
	return found && got == wantID
}

func exprWritesLexIdentInFuncLits(expr ast.Expr, name string, wantID int, scope *lexNameScope, idGen *int) bool {
	if expr == nil {
		return false
	}
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		lit, ok := n.(*ast.FuncLit)
		if !ok || lit.Body == nil {
			return true
		}
		child := newLexNameScope(scope)
		if lit.Type != nil && lit.Type.Params != nil {
			for _, field := range lit.Type.Params.List {
				for _, n := range field.Names {
					if n != nil {
						child.declare(n.Name, idGen)
					}
				}
			}
		}
		if lit.Type != nil && lit.Type.Results != nil {
			for _, field := range lit.Type.Results.List {
				for _, n := range field.Names {
					if n != nil {
						child.declare(n.Name, idGen)
					}
				}
			}
		}
		if blockWritesLexIdent(lit.Body, name, wantID, child, idGen) {
			found = true
		}
		return false
	})
	return found
}

func stmtAdvancesLexIdent(stmt ast.Stmt, name string, wantID int, scope *lexNameScope, idGen *int) bool {
	if stmt == nil || name == "" {
		return false
	}
	switch s := stmt.(type) {
	case *ast.IncDecStmt:
		return exprWritesLexIdent(s.X, name, wantID, scope) && stmtAdvancesIdent(stmt, name)
	case *ast.AssignStmt:
		if s.Tok == token.DEFINE {
			return false
		}
		if !stmtAdvancesIdent(stmt, name) {
			return false
		}
		for _, lhs := range s.Lhs {
			if exprWritesLexIdent(lhs, name, wantID, scope) {
				return true
			}
		}
	case *ast.BlockStmt:
		child := newLexNameScope(scope)
		for _, st := range s.List {
			if stmtAdvancesLexIdent(st, name, wantID, child, idGen) {
				return true
			}
		}
	}
	return false
}

func blockWritesIdent(body *ast.BlockStmt, name string) bool {
	if body == nil || name == "" {
		return false
	}
	idGen := 0
	root := newLexNameScope(nil)
	id := root.declare(name, &idGen)
	return blockWritesLexIdent(body, name, id, root, &idGen)
}

func stmtWritesIdent(stmt ast.Stmt, name string) bool {
	if stmt == nil || name == "" {
		return false
	}
	idGen := 0
	root := newLexNameScope(nil)
	id := root.declare(name, &idGen)
	return stmtWritesLexIdent(stmt, name, id, root, &idGen)
}

func condInductionIdent(cond ast.Expr) (string, bool) {
	cond = unwrapParenExpr(cond)
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok {
		return "", false
	}
	switch bin.Op {
	case token.LSS, token.LEQ, token.GTR, token.GEQ, token.EQL, token.NEQ:
		if id, ok := unwrapParenExpr(bin.X).(*ast.Ident); ok {
			return id.Name, true
		}
		if id, ok := unwrapParenExpr(bin.Y).(*ast.Ident); ok {
			return id.Name, true
		}
	}
	return "", false
}

func forInitSetsIdent(init ast.Stmt, name string) bool {
	if init == nil || name == "" {
		return false
	}
	switch s := init.(type) {
	case *ast.AssignStmt:
		for _, lhs := range s.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && id.Name == name {
				return true
			}
		}
	}
	return false
}

// inductionAdvances is retained for callers that only need a coarse "some
// update exists" check; at-most-one proofs use inductionUpdateDelta instead.
func inductionAdvances(fs *ast.ForStmt, name string) bool {
	_, _, ok := inductionUpdateDelta(fs, name)
	return ok
}

func stmtAdvancesIdent(stmt ast.Stmt, name string) bool {
	_, ok := stmtInductionDelta(stmt, name)
	return ok
}

func blockAlwaysAdvancesIdent(body *ast.BlockStmt, name string) bool {
	_, ok := blockUnconditionalInductionDelta(body, name)
	return ok
}

func condProvenSingleIteration(cond ast.Expr) bool {
	// Legacy helper kept for compatibility; direction-aware proofs use
	// provesInductionAtMostOne instead of this bound-only check.
	cond = unwrapParenExpr(cond)
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok {
		return false
	}
	switch bin.Op {
	case token.LSS:
		if n, ok := intLiteralValue(bin.Y); ok && n <= 1 {
			return true
		}
	case token.LEQ:
		if n, ok := intLiteralValue(bin.Y); ok && n < 1 {
			return true
		}
	}
	return false
}

func blockExitsBeforeBackedge(body *ast.BlockStmt) bool {
	if body == nil || len(body.List) == 0 {
		return false
	}
	hasContinue := false
	ast.Inspect(body, func(n ast.Node) bool {
		if br, ok := n.(*ast.BranchStmt); ok && br.Tok == token.CONTINUE {
			hasContinue = true
		}
		return true
	})
	if hasContinue {
		return false
	}
	bodyLabels := indexLabelsIn(body)
	// Require every path to break/return/exit-goto: conservative check.
	return stmtsAlwaysExitLoop(body.List, body, bodyLabels)
}

func stmtsAlwaysExitLoop(stmts []ast.Stmt, loopBody *ast.BlockStmt, bodyLabels map[string]*ast.LabeledStmt) bool {
	if len(stmts) == 0 {
		return false
	}
	for _, st := range stmts {
		switch s := st.(type) {
		case *ast.BranchStmt:
			if s.Tok == token.BREAK {
				return true
			}
			if s.Tok == token.CONTINUE {
				return false
			}
			if s.Tok == token.GOTO {
				return gotoExitsLoop(s, loopBody, bodyLabels)
			}
		case *ast.ReturnStmt:
			return true
		case *ast.BlockStmt:
			if stmtsAlwaysExitLoop(s.List, loopBody, bodyLabels) {
				return true
			}
		case *ast.LabeledStmt:
			if stmtsAlwaysExitLoop([]ast.Stmt{s.Stmt}, loopBody, bodyLabels) {
				return true
			}
		case *ast.IfStmt:
			thenExit := s.Body != nil && stmtsAlwaysExitLoop(s.Body.List, loopBody, bodyLabels)
			elseExit := false
			if s.Else == nil {
				elseExit = false
			} else if eb, ok := s.Else.(*ast.BlockStmt); ok {
				elseExit = stmtsAlwaysExitLoop(eb.List, loopBody, bodyLabels)
			} else if ei, ok := s.Else.(*ast.IfStmt); ok {
				elseExit = stmtsAlwaysExitLoop([]ast.Stmt{ei}, loopBody, bodyLabels)
			}
			if thenExit && elseExit {
				return true
			}
		case *ast.ForStmt, *ast.RangeStmt:
			return false
		}
	}
	switch s := stmts[len(stmts)-1].(type) {
	case *ast.BranchStmt:
		if s.Tok == token.BREAK {
			return true
		}
		if s.Tok == token.GOTO {
			return gotoExitsLoop(s, loopBody, bodyLabels)
		}
		return false
	case *ast.ReturnStmt:
		return true
	case *ast.BlockStmt:
		return stmtsAlwaysExitLoop(s.List, loopBody, bodyLabels)
	case *ast.LabeledStmt:
		return stmtsAlwaysExitLoop([]ast.Stmt{s.Stmt}, loopBody, bodyLabels)
	default:
		return false
	}
}

// gotoExitsLoop reports whether a goto targets a location outside loopBody.
// In-body targets (forward or backward) are not exits. Unknown labels do not
// prove exit (fail closed for at-most-once).
func gotoExitsLoop(br *ast.BranchStmt, loopBody *ast.BlockStmt, bodyLabels map[string]*ast.LabeledStmt) bool {
	if br == nil || br.Tok != token.GOTO || br.Label == nil {
		return false
	}
	name := br.Label.Name
	target, ok := bodyLabels[name]
	if !ok {
		// Label not defined inside this loop body ⇒ legal outer target exits,
		// or unresolved. Compiling code with an outer label exits; treating
		// missing body label as exit matches "target outside loop body".
		return true
	}
	return !labeledStmtInside(target, loopBody)
}

func loopConsumesOrResetsTimer(loop ast.Node, timerVals, timerChans map[string]bool, aliasToPath map[string]string, aliases funcValueAliases) bool {
	var body *ast.BlockStmt
	switch l := loop.(type) {
	case *ast.RangeStmt:
		if isTimerChannelExpr(l.X, timerVals, timerChans, aliasToPath, aliases) {
			return true
		}
		body = l.Body
	case *ast.ForStmt:
		body = l.Body
	default:
		return false
	}
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.UnaryExpr:
			if e.Op == token.ARROW && isTimerChannelExpr(e.X, timerVals, timerChans, aliasToPath, aliases) {
				found = true
			}
		case *ast.CallExpr:
			sel, ok := unwrapParenExpr(e.Fun).(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "Reset" {
				return true
			}
			if id, ok := unwrapParenExpr(sel.X).(*ast.Ident); ok && timerVals[id.Name] {
				found = true
			}
		}
		return true
	})
	return found
}

// Canonical function/callback identities (remediation 1.1b2b):
//   free function: {packageImportPath}.func:{name}
//   method:        {packageImportPath}.method:{canonicalReceiverType}.{method}
//   func literal:  {enclosingCanonical}.lit#{ordinal}
// Never join by bare short method name across receiver types or packages.

func freeFuncIdentity(pkgPath, name string) string {
	if name == "" {
		return ""
	}
	if pkgPath == "" {
		return "func:" + name
	}
	return pkgPath + ".func:" + name
}

func methodFuncIdentity(pkgPath, recvType, method string) string {
	if recvType == "" || method == "" {
		return ""
	}
	if pkgPath == "" {
		return "method:" + recvType + "." + method
	}
	return pkgPath + ".method:" + recvType + "." + method
}

func formatReceiverType(expr ast.Expr) string {
	expr = unwrapParenExpr(expr)
	switch e := expr.(type) {
	case *ast.StarExpr:
		inner := unwrapParenExpr(e.X)
		if id, ok := inner.(*ast.Ident); ok {
			return "*" + id.Name
		}
		if name := recvTypeLocalName(inner); name != "" {
			return "*" + name
		}
	case *ast.Ident:
		return e.Name
	default:
		return recvTypeLocalName(expr)
	}
	return ""
}

// callbackIdentity canonicalizes a function or method declaration.
func callbackIdentity(fn *ast.FuncDecl, pkgPath string) string {
	if fn == nil || fn.Name == nil {
		return ""
	}
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return freeFuncIdentity(pkgPath, fn.Name.Name)
	}
	return methodFuncIdentity(pkgPath, formatReceiverType(fn.Recv.List[0].Type), fn.Name.Name)
}

// funcLitIdentity is a stable enclosing-declaration + AST ordinal identity.
func funcLitIdentity(enclosing string, ordinal int) string {
	if enclosing == "" {
		return "lit#" + itoaAlias(ordinal)
	}
	return enclosing + ".lit#" + itoaAlias(ordinal)
}

func itoaAlias(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func resolveCallbackTarget(expr ast.Expr, enclosing *ast.FuncDecl, pkgPath string) string {
	expr = unwrapParenExpr(expr)
	switch e := expr.(type) {
	case *ast.Ident:
		return freeFuncIdentity(pkgPath, e.Name)
	case *ast.SelectorExpr:
		if e.Sel == nil {
			return ""
		}
		// Method value: p.tick / (*p).tick on the enclosing receiver.
		if enclosing != nil && enclosing.Recv != nil && len(enclosing.Recv.List) > 0 {
			recvName := ""
			if len(enclosing.Recv.List[0].Names) > 0 && enclosing.Recv.List[0].Names[0] != nil {
				recvName = enclosing.Recv.List[0].Names[0].Name
			}
			x := unwrapParenExpr(e.X)
			matchRecv := false
			if id, ok := x.(*ast.Ident); ok && id.Name == recvName {
				matchRecv = true
			}
			if star, ok := x.(*ast.StarExpr); ok {
				if id, ok := unwrapParenExpr(star.X).(*ast.Ident); ok && id.Name == recvName {
					matchRecv = true
				}
			}
			if matchRecv {
				fake := &ast.FuncDecl{Name: e.Sel, Recv: enclosing.Recv}
				return callbackIdentity(fake, pkgPath)
			}
		}
		// Method expression: Type.method / (*Type).method.
		recvType := formatReceiverType(e.X)
		if recvType != "" {
			return methodFuncIdentity(pkgPath, recvType, e.Sel.Name)
		}
	}
	return ""
}

// callbackScheduleEdges maps callback identity -> scheduled callback identities
// via time.AfterFunc. A cycle is required for self-rescheduling poll detection.
func buildAfterFuncScheduleEdges(files []*parsedOverlayFile) map[string][]string {
	edges := map[string][]string{}
	add := func(from, to string) {
		if from == "" || to == "" {
			return
		}
		if slices.Contains(edges[from], to) {
			return
		}
		edges[from] = append(edges[from], to)
	}
	litOrdinal := map[string]int{}
	for _, pf := range files {
		aliasToPath := pf.aliasToPath
		aliases := collectFuncValueAliases(pf.file, aliasToPath)
		for _, decl := range pf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name == nil {
				continue
			}
			enclosing := callbackIdentity(fn, pf.pkgPath)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name, ok := timeCallName(call, aliasToPath, aliases)
				if !ok || name != "AfterFunc" || len(call.Args) < 2 {
					return true
				}
				cb := unwrapParenExpr(call.Args[1])
				switch c := cb.(type) {
				case *ast.Ident:
					target := freeFuncIdentity(pf.pkgPath, c.Name)
					add(enclosing, target)
					if lit := findAssignedFuncLit(fn, c.Name); lit != nil {
						ord := litOrdinal[enclosing]
						litOrdinal[enclosing] = ord + 1
						litID := funcLitIdentity(enclosing, ord)
						add(enclosing, litID)
						for _, t := range afterFuncTargetsIn(lit.Body, fn, pf.pkgPath, aliasToPath, aliases) {
							add(litID, t)
							add(target, t)
						}
					}
				case *ast.SelectorExpr:
					target := resolveCallbackTarget(c, fn, pf.pkgPath)
					add(enclosing, target)
				case *ast.FuncLit:
					ord := litOrdinal[enclosing]
					litOrdinal[enclosing] = ord + 1
					litID := funcLitIdentity(enclosing, ord)
					add(enclosing, litID)
					for _, target := range afterFuncTargetsIn(c.Body, fn, pf.pkgPath, aliasToPath, aliases) {
						add(litID, target)
						add(enclosing, target)
					}
				}
				return true
			})
		}
	}
	return edges
}

func appendUnique(slice []string, v string) []string {
	if v == "" {
		return slice
	}
	if slices.Contains(slice, v) {
		return slice
	}
	return append(slice, v)
}

func findAssignedFuncLit(fn *ast.FuncDecl, name string) *ast.FuncLit {
	var lit *ast.FuncLit
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || id.Name != name || i >= len(assign.Rhs) {
				continue
			}
			if fl, ok := unwrapParenExpr(assign.Rhs[i]).(*ast.FuncLit); ok {
				lit = fl
			}
		}
		return true
	})
	return lit
}

func afterFuncTargetsIn(body *ast.BlockStmt, enclosing *ast.FuncDecl, pkgPath string, aliasToPath map[string]string, aliases funcValueAliases) []string {
	if body == nil {
		return nil
	}
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name, ok := timeCallName(call, aliasToPath, aliases)
		if !ok || name != "AfterFunc" || len(call.Args) < 2 {
			return true
		}
		cb := unwrapParenExpr(call.Args[1])
		switch c := cb.(type) {
		case *ast.Ident:
			out = append(out, freeFuncIdentity(pkgPath, c.Name))
		case *ast.SelectorExpr:
			if t := resolveCallbackTarget(c, enclosing, pkgPath); t != "" {
				out = append(out, t)
			}
		}
		return true
	})
	return out
}

// callbackNodeInRecurrentCycle reports whether node itself participates in a
// recurrent SCC (can reach itself), not merely whether it can reach some cycle.
func callbackNodeInRecurrentCycle(edges map[string][]string, node string) bool {
	if node == "" {
		return false
	}
	// Self-loop.
	if slices.Contains(edges[node], node) {
		return true
	}
	visited := map[string]bool{}
	var dfs func(string) bool
	dfs = func(n string) bool {
		if visited[n] {
			return false
		}
		visited[n] = true
		for _, next := range edges[n] {
			if next == node {
				return true
			}
			if dfs(next) {
				return true
			}
		}
		return false
	}
	for _, next := range edges[node] {
		visited = map[string]bool{}
		if dfs(next) {
			return true
		}
	}
	return false
}

func recurrentCallbackComponent(edges map[string][]string, node string) []string {
	if !callbackNodeInRecurrentCycle(edges, node) {
		return nil
	}
	// Nodes mutually reachable with node.
	var members []string
	seen := map[string]bool{}
	canReachFromNode := map[string]bool{node: true}
	var dfsOut func(string)
	dfsOut = func(n string) {
		for _, next := range edges[n] {
			if canReachFromNode[next] {
				continue
			}
			canReachFromNode[next] = true
			dfsOut(next)
		}
	}
	dfsOut(node)
	reachesNode := func(start string) bool {
		if start == node {
			return true
		}
		vis := map[string]bool{}
		var dfs func(string) bool
		dfs = func(n string) bool {
			if n == node {
				return true
			}
			if vis[n] {
				return false
			}
			vis[n] = true
			return slices.ContainsFunc(edges[n], dfs)
		}
		return dfs(start)
	}
	names := make([]string, 0, len(canReachFromNode))
	for n := range canReachFromNode {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if reachesNode(n) {
			if !seen[n] {
				seen[n] = true
				members = append(members, n)
			}
		}
	}
	return members
}

func funcHasConfigEvidence(fn *ast.FuncDecl, filename string, aliasToPath map[string]string, aliases funcValueAliases, idx *configPathPkgIndex) bool {
	if fn == nil || fn.Body == nil {
		return false
	}
	scope := seedPathScopeForFunc(fn, idx)
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if callHasConfigSourceEvidence(call, filename, aliasToPath, aliases, idx, scope, fn) {
			found = true
		}
		return true
	})
	return found
}

func seedPathScopeForFunc(fn *ast.FuncDecl, idx *configPathPkgIndex) *pathScope {
	scope := newPathScope(nil)
	if idx != nil {
		names := make([]string, 0, len(idx.pkgVals))
		for name := range idx.pkgVals {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if idx.pkgVals[name] {
				scope.declare(name, true)
			}
		}
	}
	if fn == nil {
		return scope
	}
	fnID := callbackIdentity(fn, "")
	short := ""
	if fn.Name != nil {
		short = fn.Name.Name
	}
	if fn.Type != nil && fn.Type.Params != nil {
		pos := 0
		for _, field := range fn.Type.Params.List {
			for _, name := range field.Names {
				if name == nil {
					pos++
					continue
				}
				isCfg := false
				if idx != nil {
					isCfg = idx.funcParamIsConfig(short, name.Name) ||
						idx.funcParamIsConfig(fnID, name.Name) ||
						idx.funcParamIsConfig(short, fmt.Sprintf("#%d", pos)) ||
						idx.funcParamIsConfig(fnID, fmt.Sprintf("#%d", pos))
					if !isCfg && idx.adapter && looksLikeConfigPathName(name.Name) {
						isCfg = true
					}
				}
				scope.declare(name.Name, isCfg)
				pos++
			}
		}
	}
	return scope
}

type pollPkgContext struct {
	pathIndex      *configPathPkgIndex
	callbackEdges  map[string][]string
	callGraph      map[string][]string // canonical caller -> in-package callees
	reachesConfig  map[string]bool     // fixpoint: transitively reaches a config probe
	funcsByID      map[string]*ast.FuncDecl
	fileByFunc     map[*ast.FuncDecl]string
	aliasByFile    map[string]map[string]string
	funcAliases    map[string]funcValueAliases
	pkgShadowNames map[string]bool // package-scope declared identifiers (shadow predeclareds)
	pkgPath        string
}

// indexPackageScopeShadowNames collects package-level declared names across every
// overlay/production file in the package. Methods (with receivers) do not enter
// package scope and are excluded.
func indexPackageScopeShadowNames(files []*parsedOverlayFile) map[string]bool {
	names := map[string]bool{}
	for _, pf := range files {
		if pf == nil || pf.file == nil {
			continue
		}
		for _, decl := range pf.file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name != nil && d.Name.Name != "_" {
					names[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch sp := spec.(type) {
					case *ast.ValueSpec:
						for _, n := range sp.Names {
							if n != nil && n.Name != "_" {
								names[n.Name] = true
							}
						}
					case *ast.TypeSpec:
						if sp.Name != nil && sp.Name.Name != "_" {
							names[sp.Name.Name] = true
						}
					}
				}
			}
		}
	}
	return names
}

func buildPollPkgContext(files []*parsedOverlayFile) (*pollPkgContext, error) {
	pathIndex, err := buildConfigPathPkgIndex(files)
	if err != nil {
		return nil, err
	}
	pkgPath := ""
	if len(files) > 0 {
		pkgPath = files[0].pkgPath
	}
	ctx := &pollPkgContext{
		pathIndex:      pathIndex,
		callbackEdges:  buildAfterFuncScheduleEdges(files),
		callGraph:      map[string][]string{},
		reachesConfig:  map[string]bool{},
		funcsByID:      map[string]*ast.FuncDecl{},
		fileByFunc:     map[*ast.FuncDecl]string{},
		aliasByFile:    map[string]map[string]string{},
		funcAliases:    map[string]funcValueAliases{},
		pkgShadowNames: indexPackageScopeShadowNames(files),
		pkgPath:        pkgPath,
	}
	for _, pf := range files {
		ctx.aliasByFile[pf.filename] = pf.aliasToPath
		ctx.funcAliases[pf.filename] = collectFuncValueAliases(pf.file, pf.aliasToPath)
		for _, decl := range pf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil {
				continue
			}
			id := callbackIdentity(fn, pf.pkgPath)
			ctx.funcsByID[id] = fn
			ctx.fileByFunc[fn] = pf.filename
			indexFuncSignatureParams(fn, ctx.pathIndex)
		}
	}
	// Bind call-site literals to named params, then re-propagate field evidence
	// from constructors that assign those params into struct fields.
	remapPositionalConfigParams(files, ctx.pathIndex)
	if err := propagateConfigPathFields(files, ctx.pathIndex); err != nil {
		return nil, err
	}
	ctx.callGraph = buildInPackageCallGraph(files, ctx)
	ctx.reachesConfig = computeTransitiveConfigReach(ctx)
	return ctx, nil
}

func propagateConfigPathFields(files []*parsedOverlayFile, idx *configPathPkgIndex) error {
	if idx == nil {
		return nil
	}
	factBound := configPathFactBound(files)
	for step := 0; ; step++ {
		if step > factBound {
			return fmt.Errorf("config-path field provenance fixpoint exceeded bound %d", factBound)
		}
		grew := false
		for _, pf := range files {
			for _, decl := range pf.file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				if indexConfigPathFuncOrdered(fn, idx) {
					grew = true
				}
			}
		}
		if !grew {
			return nil
		}
	}
}

// configPathFactBound returns a termination bound derived from the finite set of
// markable param/field facts in files (plus one). Each fixpoint step must add at
// least one new fact when it continues, so exceeding this bound is inconsistent.
func configPathFactBound(files []*parsedOverlayFile) int {
	bound := 1
	for _, pf := range files {
		if pf == nil || pf.file == nil {
			continue
		}
		for _, decl := range pf.file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Type != nil && d.Type.Params != nil {
					for _, field := range d.Type.Params.List {
						n := len(field.Names)
						if n == 0 {
							n = 1
						}
						bound += n
						// Positional aliases (#i) may also be marked.
						bound += n
					}
				}
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil {
						continue
					}
					for _, field := range st.Fields.List {
						n := len(field.Names)
						if n == 0 {
							n = 1
						}
						bound += n
					}
				}
			}
		}
	}
	if bound < 1 {
		return 1
	}
	return bound
}

func indexFuncSignatureParams(fn *ast.FuncDecl, idx *configPathPkgIndex) {
	if fn == nil || fn.Name == nil || fn.Type == nil || fn.Type.Params == nil || idx == nil {
		return
	}
	short := fn.Name.Name
	fnID := callbackIdentity(fn, "")
	pos := 0
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if name == nil {
				pos++
				continue
			}
			key := fmt.Sprintf("#%d", pos)
			if idx.funcParams[short][key] || idx.funcParams[fnID][key] {
				idx.markFuncParam(short, name.Name)
				idx.markFuncParam(fnID, name.Name)
			}
			pos++
		}
	}
}

func remapPositionalConfigParams(files []*parsedOverlayFile, idx *configPathPkgIndex) {
	if idx == nil {
		return
	}
	for _, pf := range files {
		for _, decl := range pf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Type == nil || fn.Type.Params == nil {
				continue
			}
			indexFuncSignatureParams(fn, idx)
			_ = pf
		}
	}
	// Re-scan call sites now that signatures exist, binding args to named params.
	for _, pf := range files {
		for _, decl := range pf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				fun, ok := unwrapParenExpr(call.Fun).(*ast.Ident)
				if !ok {
					return true
				}
				target := fun.Name
				// Find callee signature in package.
				var callee *ast.FuncDecl
				for _, peer := range files {
					for _, d := range peer.file.Decls {
						f, ok := d.(*ast.FuncDecl)
						if !ok || f.Name == nil || f.Recv != nil || f.Name.Name != target {
							continue
						}
						callee = f
						break
					}
					if callee != nil {
						break
					}
				}
				if callee == nil || callee.Type == nil || callee.Type.Params == nil {
					return true
				}
				pos := 0
				for _, field := range callee.Type.Params.List {
					for _, name := range field.Names {
						if name == nil {
							pos++
							continue
						}
						if pos < len(call.Args) && exprHasConfigPathEvidence(call.Args[pos], idx, nil, fn, "") {
							idx.markFuncParam(target, name.Name)
							idx.markFuncParam(target, fmt.Sprintf("#%d", pos))
						}
						pos++
					}
					if len(field.Names) == 0 {
						pos++
					}
				}
				return true
			})
		}
	}
}

func cycleHasConfigEvidence(ctx *pollPkgContext, members []string) bool {
	if ctx == nil {
		return false
	}
	seen := map[string]bool{}
	for _, m := range members {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		// Config on the SCC node itself or any callee reachable from it during recurrence.
		if ctx.reachesConfig[m] {
			return true
		}
	}
	return false
}

// buildInPackageCallGraph records canonical free-function and method call edges
// within a package. Edges are deterministic (callees sorted per caller).
func buildInPackageCallGraph(files []*parsedOverlayFile, ctx *pollPkgContext) map[string][]string {
	edges := map[string][]string{}
	if ctx == nil {
		return edges
	}
	add := func(from, to string) {
		if from == "" || to == "" || from == to {
			return
		}
		if slices.Contains(edges[from], to) {
			return
		}
		edges[from] = append(edges[from], to)
	}
	for _, pf := range files {
		for _, decl := range pf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name == nil {
				continue
			}
			from := callbackIdentity(fn, pf.pkgPath)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				to := resolveInPackageCallTarget(call, fn, pf.pkgPath, ctx)
				add(from, to)
				return true
			})
		}
	}
	for from, tos := range edges {
		sort.Strings(tos)
		edges[from] = tos
	}
	return edges
}

func resolveInPackageCallTarget(call *ast.CallExpr, enclosing *ast.FuncDecl, pkgPath string, ctx *pollPkgContext) string {
	if call == nil {
		return ""
	}
	fun := unwrapParenExpr(call.Fun)
	switch f := fun.(type) {
	case *ast.Ident:
		id := freeFuncIdentity(pkgPath, f.Name)
		if ctx != nil && ctx.funcsByID[id] != nil {
			return id
		}
		return ""
	case *ast.SelectorExpr:
		id := resolveCallbackTarget(f, enclosing, pkgPath)
		if ctx != nil && ctx.funcsByID[id] != nil {
			return id
		}
		// Method call on receiver-shaped selector may use value/pointer variant.
		if id == "" {
			return ""
		}
		if ctx != nil && ctx.funcsByID[id] == nil {
			// Try flipping pointer/value receiver form.
			alt := flipMethodIdentityPointer(id)
			if ctx.funcsByID[alt] != nil {
				return alt
			}
		}
		return id
	}
	return ""
}

func flipMethodIdentityPointer(id string) string {
	// ...method:*T.m <-> ...method:T.m
	const key = ".method:"
	i := strings.Index(id, key)
	if i < 0 {
		if after, ok := strings.CutPrefix(id, "method:"); ok {
			rest := after
			if after, ok := strings.CutPrefix(rest, "*"); ok {
				return "method:" + after
			}
			dot := strings.Index(rest, ".")
			if dot > 0 {
				return "method:*" + rest
			}
		}
		return id
	}
	prefix := id[:i+len(key)]
	rest := id[i+len(key):]
	if after, ok := strings.CutPrefix(rest, "*"); ok {
		return prefix + after
	}
	return prefix + "*" + rest
}

// computeTransitiveConfigReach fixpoints which canonical functions transitively
// reach a direct config-source probe. Does not by itself emit poll violations.
func computeTransitiveConfigReach(ctx *pollPkgContext) map[string]bool {
	out := map[string]bool{}
	if ctx == nil {
		return out
	}
	ids := make([]string, 0, len(ctx.funcsByID))
	for id := range ctx.funcsByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fn := ctx.funcsByID[id]
		filename := ctx.fileByFunc[fn]
		if funcHasConfigEvidence(fn, filename, ctx.aliasByFile[filename], ctx.funcAliases[filename], ctx.pathIndex) {
			out[id] = true
		}
	}
	bound := len(ids) + 1
	for range bound {
		grew := false
		for _, id := range ids {
			if out[id] {
				continue
			}
			for _, cal := range ctx.callGraph[id] {
				if out[cal] {
					out[id] = true
					grew = true
					break
				}
			}
		}
		if !grew {
			break
		}
	}
	return out
}

func findConfigWatcherMechanisms(fset *token.FileSet, f *ast.File, filename string, ctx *pollPkgContext) []string {
	if isLegitimateRefreshAllowPath(filename) {
		return nil
	}
	aliasToPath := importAliasToPath(f)
	aliases := collectFuncValueAliases(f, aliasToPath)
	var out []string
	for _, path := range fileImportPaths(f) {
		if isWatcherImportPath(path) {
			out = append(out, "import "+path)
		}
	}

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if label, ok := callViaFuncAlias(call, aliases); ok {
			switch {
			case strings.Contains(label, "fsnotify") && strings.HasSuffix(label, ".NewWatcher"):
				out = append(out, formatPos(fset, call.Pos())+": fsnotify.NewWatcher (func-value alias)")
			case strings.Contains(label, "fsnotify") && strings.HasSuffix(label, ".NewBufferedWatcher"):
				out = append(out, formatPos(fset, call.Pos())+": fsnotify.NewBufferedWatcher (func-value alias)")
			case strings.Contains(label, "rjeczalik/notify") || strings.HasSuffix(label, "/notify.Watch"):
				out = append(out, formatPos(fset, call.Pos())+": notify.Watch (func-value alias)")
			}
			return true
		}
		if id, ok := unwrapParenExpr(call.Fun).(*ast.Ident); ok {
			switch id.Name {
			case "WatchConfig", "Watch", "OnConfigChange":
				out = append(out, formatPos(fset, call.Pos())+": "+id.Name)
			}
			return true
		}
		recv, name, ok := callSelector(call)
		if !ok {
			return true
		}
		path := resolveImportPath(aliasToPath, recv)
		switch {
		case name == "WatchConfig" || name == "OnConfigChange" || (name == "Watch" && isWatcherImportPath(path)):
			out = append(out, formatPos(fset, call.Pos())+": "+recv+"."+name)
		case strings.Contains(path, "fsnotify") && (name == "NewWatcher" || name == "NewBufferedWatcher"):
			out = append(out, formatPos(fset, call.Pos())+": fsnotify."+name)
		case strings.Contains(path, "rjeczalik/notify") || (strings.HasSuffix(path, "/notify") && name == "Watch"):
			out = append(out, formatPos(fset, call.Pos())+": notify."+name)
		}
		return true
	})

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		out = append(out, findPollWatchInFunc(fset, fn, filename, aliasToPath, aliases, ctx)...)
	}
	return out
}

func findPollWatchInFunc(fset *token.FileSet, fn *ast.FuncDecl, filename string, aliasToPath map[string]string, aliases funcValueAliases, ctx *pollPkgContext) []string {
	var idx *configPathPkgIndex
	var callbackEdges map[string][]string
	var pkgShadows map[string]bool
	if ctx != nil {
		idx = ctx.pathIndex
		callbackEdges = ctx.callbackEdges
		pkgShadows = ctx.pkgShadowNames
	}
	timerVals := collectTimerValuesInFunc(fn, aliasToPath, aliases)
	timerChans := collectTimerChannelsInFunc(fn, timerVals, aliasToPath, aliases)

	var out []string
	root := seedPathScopeForFunc(fn, idx)
	live := newLiveFuncAliases(nil)
	pkgPath := packagePathFromFilename(filename)
	if ctx != nil && ctx.pkgPath != "" {
		pkgPath = ctx.pkgPath
	}
	analyzePollBlock(fset, fn.Body, root, live, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, &out)

	// Function literals nest loops outside ordinary statement walking; analyze
	// their bodies with the same package shadow index so panic params/locals resolve.
	if fn.Body != nil {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.FuncLit)
			if !ok || lit.Body == nil {
				return true
			}
			child := newPathScope(root)
			childLive := newLiveFuncAliases(live)
			analyzePollBlock(fset, lit.Body, child, childLive, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, &out)
			return true
		})
	}

	// Named / multi-callback AfterFunc cycles: require the enclosing node to be
	// inside the recurrent SCC and config evidence on that SCC (or callees).
	fnID := callbackIdentity(fn, packagePathFromFilename(filename))
	inCycle := false
	var members []string
	if callbackNodeInRecurrentCycle(callbackEdges, fnID) {
		inCycle = true
		members = recurrentCallbackComponent(callbackEdges, fnID)
	}
	if inCycle && cycleHasConfigEvidence(ctx, members) {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if name, ok := timeCallName(call, aliasToPath, aliases); ok && name == "AfterFunc" {
				out = append(out, formatPos(fset, call.Pos())+": time.AfterFunc config-reload poller")
			}
			if pname, ok := osProbeCallName(call, aliasToPath, aliases); ok {
				scope := seedPathScopeForFunc(fn, idx)
				if callHasConfigSourceEvidence(call, filename, aliasToPath, aliases, idx, scope, fn) {
					label := "os." + pname
					if _, aliased := callViaFuncAlias(call, aliases); aliased {
						label += " (func-value alias)"
					}
					out = append(out, formatPos(fset, call.Pos())+": "+label+" config-source probe")
				}
			}
			return true
		})
	}

	// Local func-value self-reschedule: var tick func(); tick = func(){ ...; AfterFunc(d, tick) }
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || i >= len(assign.Rhs) {
				continue
			}
			lit, ok := unwrapParenExpr(assign.Rhs[i]).(*ast.FuncLit)
			if !ok || lit.Body == nil {
				continue
			}
			targets := afterFuncTargetsIn(lit.Body, fn, packagePathFromFilename(filename), aliasToPath, aliases)
			self := false
			for _, t := range targets {
				if t == id.Name || t == freeFuncIdentity(packagePathFromFilename(filename), id.Name) {
					self = true
					break
				}
			}
			if !self {
				continue
			}
			scope := seedPathScopeForFunc(fn, idx)
			hasEv := false
			ast.Inspect(lit.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if callHasConfigSourceEvidence(call, filename, aliasToPath, aliases, idx, scope, fn) {
					hasEv = true
				}
				return true
			})
			if !hasEv {
				continue
			}
			ast.Inspect(lit.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if name, ok := timeCallName(call, aliasToPath, aliases); ok && name == "AfterFunc" {
					out = append(out, formatPos(fset, call.Pos())+": time.AfterFunc config-reload poller")
				}
				if pname, ok := osProbeCallName(call, aliasToPath, aliases); ok {
					if callHasConfigSourceEvidence(call, filename, aliasToPath, aliases, idx, scope, fn) {
						label := "os." + pname
						if _, aliased := callViaFuncAlias(call, aliases); aliased {
							label += " (func-value alias)"
						}
						out = append(out, formatPos(fset, call.Pos())+": "+label+" config-source probe")
					}
				}
				return true
			})
		}
		return true
	})

	return out
}

func analyzePollBlock(fset *token.FileSet, block *ast.BlockStmt, scope *pathScope, live *liveFuncAliases, fn *ast.FuncDecl, filename string, aliasToPath map[string]string, aliases funcValueAliases, idx *configPathPkgIndex, timerVals, timerChans map[string]bool, pkgShadows map[string]bool, ctx *pollPkgContext, pkgPath string, out *[]string) {
	if block == nil {
		return
	}
	for _, stmt := range block.List {
		analyzePollStmt(fset, stmt, scope, live, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, out)
	}
}

func analyzePollStmt(fset *token.FileSet, stmt ast.Stmt, scope *pathScope, live *liveFuncAliases, fn *ast.FuncDecl, filename string, aliasToPath map[string]string, aliases funcValueAliases, idx *configPathPkgIndex, timerVals, timerChans map[string]bool, pkgShadows map[string]bool, ctx *pollPkgContext, pkgPath string, out *[]string) {
	switch s := stmt.(type) {
	case *ast.DeclStmt:
		if gen, ok := s.Decl.(*ast.GenDecl); ok {
			analyzePathGenDecl(gen, scope, idx, fn)
			for _, spec := range gen.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					applyLiveFuncAliasValueSpec(vs, live, aliasToPath, pkgPath)
				}
			}
		}
	case *ast.AssignStmt:
		analyzePathAssign(s, scope, idx, fn)
		applyLiveFuncAliasAssign(s, live, aliasToPath, pkgPath)
	case *ast.BlockStmt:
		child := newPathScope(scope)
		analyzePollBlock(fset, s, child, newLiveFuncAliases(live), fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, out)
	case *ast.LabeledStmt:
		labels, core := peelLabeledStmt(s)
		switch c := core.(type) {
		case *ast.ForStmt:
			recordPollLoop(fset, c, scope, live, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, out, labels...)
			applyForInitToOuterScope(c, scope, idx, fn)
		case *ast.RangeStmt:
			recordPollLoop(fset, c, scope, live, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, out, labels...)
			applyRangeBodyEffectsToOuterScope(c, scope, idx, fn, filename, aliasToPath, aliases, fset, timerVals, timerChans, out)
		default:
			analyzePollStmt(fset, core, scope, live, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, out)
		}
	case *ast.IfStmt:
		ifScope := newPathScope(scope)
		ifLive := live
		if s.Init != nil {
			analyzePollStmt(fset, s.Init, ifScope, ifLive, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, out)
		}
		_, preexisting := forkPathEnv(ifScope)
		thenEnv, _ := forkPathEnv(ifScope)
		thenLive, preAlias := forkLiveFuncAliases(ifLive)
		analyzePollBlock(fset, s.Body, thenEnv, thenLive, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, out)
		var elseEnv *pathScope
		var elseLive *liveFuncAliases
		if s.Else == nil {
			elseEnv = snapshotPathEnv(ifScope, preexisting)
			elseLive, _ = forkLiveFuncAliases(ifLive)
		} else if eb, ok := s.Else.(*ast.BlockStmt); ok {
			elseEnv, _ = forkPathEnv(ifScope)
			elseLive, _ = forkLiveFuncAliases(ifLive)
			analyzePollBlock(fset, eb, elseEnv, elseLive, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, out)
		} else {
			elseEnv, _ = forkPathEnv(ifScope)
			elseLive, _ = forkLiveFuncAliases(ifLive)
			analyzePollStmt(fset, s.Else, elseEnv, elseLive, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, out)
		}
		mergePathEnvs(ifScope, preexisting, thenEnv, elseEnv)
		mergeLiveFuncAliases(ifLive, preAlias, thenLive, elseLive)
	case *ast.ForStmt:
		recordPollLoop(fset, s, scope, live, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, out)
		applyForInitToOuterScope(s, scope, idx, fn)
	case *ast.RangeStmt:
		recordPollLoop(fset, s, scope, live, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, out)
		applyRangeBodyEffectsToOuterScope(s, scope, idx, fn, filename, aliasToPath, aliases, fset, timerVals, timerChans, out)
	case *ast.SwitchStmt:
		swScope := newPathScope(scope)
		if s.Init != nil {
			analyzePollStmt(fset, s.Init, swScope, live, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, out)
		}
		_, preexisting := forkPathEnv(swScope)
		var branches []*pathScope
		hasDefault := false
		if s.Body != nil {
			for _, clause := range s.Body.List {
				cc, ok := clause.(*ast.CaseClause)
				if !ok {
					continue
				}
				if cc.List == nil {
					hasDefault = true
				}
				br, _ := forkPathEnv(swScope)
				brLive, _ := forkLiveFuncAliases(live)
				analyzePollBlock(fset, &ast.BlockStmt{List: cc.Body}, br, brLive, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, out)
				branches = append(branches, br)
			}
		}
		if len(branches) == 0 {
			branches = append(branches, snapshotPathEnv(swScope, preexisting))
		}
		if !hasDefault {
			branches = append(branches, snapshotPathEnv(swScope, preexisting))
		}
		mergePathEnvs(swScope, preexisting, branches...)
	case *ast.TypeSwitchStmt:
		swScope := newPathScope(scope)
		if s.Init != nil {
			analyzePollStmt(fset, s.Init, swScope, live, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, out)
		}
		if s.Assign != nil {
			analyzePollStmt(fset, s.Assign, swScope, live, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, out)
		}
		_, preexisting := forkPathEnv(swScope)
		var branches []*pathScope
		hasDefault := false
		if s.Body != nil {
			for _, clause := range s.Body.List {
				cc, ok := clause.(*ast.CaseClause)
				if !ok {
					continue
				}
				if cc.List == nil {
					hasDefault = true
				}
				br, _ := forkPathEnv(swScope)
				brLive, _ := forkLiveFuncAliases(live)
				analyzePollBlock(fset, &ast.BlockStmt{List: cc.Body}, br, brLive, fn, filename, aliasToPath, aliases, idx, timerVals, timerChans, pkgShadows, ctx, pkgPath, out)
				branches = append(branches, br)
			}
		}
		if !hasDefault {
			branches = append(branches, snapshotPathEnv(swScope, preexisting))
		}
		if len(branches) > 0 {
			mergePathEnvs(swScope, preexisting, branches...)
		}
	}
}

// applyForInitToOuterScope applies Go for-statement scope: Init runs in a
// construct-local child scope. := declarations disappear afterward; = updates
// to outer declarations persist. A statically false condition skips body/post.
func applyForInitToOuterScope(fs *ast.ForStmt, scope *pathScope, idx *configPathPkgIndex, fn *ast.FuncDecl) {
	if fs == nil || scope == nil {
		return
	}
	outerIDs := visiblePathEntryIDs(scope)
	outerNames := map[string]struct{}{}
	for name := range outerIDs {
		outerNames[name] = struct{}{}
	}
	forScope := newPathScope(scope)
	if fs.Init != nil {
		if assign, ok := fs.Init.(*ast.AssignStmt); ok {
			analyzePathAssign(assign, forScope, idx, fn)
		} else if ds, ok := fs.Init.(*ast.DeclStmt); ok {
			if gen, ok := ds.Decl.(*ast.GenDecl); ok {
				analyzePathGenDecl(gen, forScope, idx, fn)
			}
		}
	}
	// = init already mutated outer decls via lookup. := locals live in forScope only.
	if condStaticallyFalse(fs.Cond) {
		return
	}
	postInit := snapshotPathEnvByID(scope, outerIDs)
	bodyEnv := clonePathScopeTree(forScope)
	if fs.Body != nil {
		applyPathBlockAssigns(fs.Body, bodyEnv, idx, fn)
	}
	if fs.Post != nil {
		applyPathPostStmt(fs.Post, bodyEnv, idx, fn)
	}
	bodySnap := snapshotPathEnvByID(bodyEnv, outerIDs)
	branches := []*pathScope{bodySnap}
	if !condStaticallyTrue(fs.Cond) {
		branches = append(branches, postInit)
	}
	if len(outerNames) > 0 {
		mergePathEnvs(scope, outerNames, branches...)
	}
}

func applyRangeBodyEffectsToOuterScope(rs *ast.RangeStmt, scope *pathScope, idx *configPathPkgIndex, fn *ast.FuncDecl, filename string, aliasToPath map[string]string, aliases funcValueAliases, fset *token.FileSet, timerVals, timerChans map[string]bool, out *[]string) {
	if rs == nil || scope == nil {
		return
	}
	_ = filename
	_ = aliasToPath
	_ = aliases
	_ = fset
	_ = timerVals
	_ = timerChans
	_ = out
	outerIDs := visiblePathEntryIDs(scope)
	outerNames := map[string]struct{}{}
	for name := range outerIDs {
		outerNames[name] = struct{}{}
	}
	rangeScope := newPathScope(scope)
	if rs.Tok == token.DEFINE {
		if id, ok := rs.Key.(*ast.Ident); ok {
			rangeScope.declare(id.Name, false)
		}
		if id, ok := rs.Value.(*ast.Ident); ok {
			rangeScope.declare(id.Name, false)
		}
	}
	postEntry := snapshotPathEnvByID(scope, outerIDs)
	bodyEnv := clonePathScopeTree(rangeScope)
	if rs.Body != nil {
		applyPathBlockAssigns(rs.Body, bodyEnv, idx, fn)
	}
	bodySnap := snapshotPathEnvByID(bodyEnv, outerIDs)
	if len(outerNames) > 0 {
		mergePathEnvs(scope, outerNames, bodySnap, postEntry)
	}
}

// applyPathBlockAssigns updates pathScope through decls/assigns in stmt order
// without recording probes (used for post-loop outer state).
func applyPathBlockAssigns(block *ast.BlockStmt, scope *pathScope, idx *configPathPkgIndex, fn *ast.FuncDecl) {
	if block == nil {
		return
	}
	var walkStmt func(ast.Stmt, *pathScope)
	var walkBlock func(*ast.BlockStmt, *pathScope)
	walkStmt = func(stmt ast.Stmt, sc *pathScope) {
		if stmt == nil {
			return
		}
		switch s := stmt.(type) {
		case *ast.DeclStmt:
			if gen, ok := s.Decl.(*ast.GenDecl); ok {
				analyzePathGenDecl(gen, sc, idx, fn)
			}
		case *ast.AssignStmt:
			analyzePathAssign(s, sc, idx, fn)
		case *ast.BlockStmt:
			walkBlock(s, newPathScope(sc))
		case *ast.LabeledStmt:
			walkStmt(s.Stmt, sc)
		case *ast.IfStmt:
			ifScope := newPathScope(sc)
			if s.Init != nil {
				walkStmt(s.Init, ifScope)
			}
			_, preexisting := forkPathEnv(ifScope)
			thenEnv, _ := forkPathEnv(ifScope)
			walkBlock(s.Body, thenEnv)
			var elseEnv *pathScope
			if s.Else == nil {
				elseEnv = snapshotPathEnv(ifScope, preexisting)
			} else {
				elseEnv, _ = forkPathEnv(ifScope)
				walkStmt(s.Else, elseEnv)
			}
			mergePathEnvs(ifScope, preexisting, thenEnv, elseEnv)
		case *ast.ForStmt:
			applyForInitToOuterScope(s, sc, idx, fn)
		case *ast.RangeStmt:
			applyRangeBodyEffectsToOuterScope(s, sc, idx, fn, "", nil, nil, nil, nil, nil, nil)
		}
	}
	walkBlock = func(block *ast.BlockStmt, sc *pathScope) {
		if block == nil {
			return
		}
		for _, st := range block.List {
			walkStmt(st, sc)
		}
	}
	walkBlock(block, scope)
}

func analyzePathGenDecl(gen *ast.GenDecl, scope *pathScope, idx *configPathPkgIndex, fn *ast.FuncDecl) {
	if gen == nil || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
		return
	}
	for _, spec := range gen.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, name := range vs.Names {
			if name == nil {
				continue
			}
			isCfg := false
			if i < len(vs.Values) {
				isCfg = exprHasConfigPathEvidence(vs.Values[i], idx, scope, fn, "")
			}
			scope.declare(name.Name, isCfg)
		}
	}
}

func analyzePathAssign(assign *ast.AssignStmt, scope *pathScope, idx *configPathPkgIndex, fn *ast.FuncDecl) {
	if assign == nil {
		return
	}
	define := assign.Tok == token.DEFINE
	// Go assignment evaluates all RHS against the pre-assignment state, then
	// commits LHS updates together by declaration identity.
	if len(assign.Lhs) != len(assign.Rhs) {
		// Multi-value cardinality mismatch: fail safe — clear/declare non-config.
		for _, lhs := range assign.Lhs {
			id, ok := unwrapParenExpr(lhs).(*ast.Ident)
			if !ok || id.Name == "" || id.Name == "_" {
				continue
			}
			if define {
				if _, inCurrent := scope.decls[id.Name]; !inCurrent {
					scope.declare(id.Name, false)
				} else {
					scope.decls[id.Name].setConfig(false)
				}
			} else if d := scope.lookup(id.Name); d != nil {
				d.setConfig(false)
			}
		}
		return
	}
	cfgs := make([]bool, len(assign.Lhs))
	for i, rhs := range assign.Rhs {
		if looksLikeCacheOrNonConfigPath(rhs) {
			cfgs[i] = false
		} else {
			cfgs[i] = exprHasConfigPathEvidence(rhs, idx, scope, fn, "")
		}
	}
	for i, lhs := range assign.Lhs {
		isCfg := cfgs[i]
		switch l := unwrapParenExpr(lhs).(type) {
		case *ast.Ident:
			if define {
				if _, inCurrent := scope.decls[l.Name]; inCurrent {
					scope.decls[l.Name].setConfig(isCfg)
				} else {
					scope.declare(l.Name, isCfg)
				}
			} else if d := scope.lookup(l.Name); d != nil {
				d.setConfig(isCfg)
			} else {
				scope.declare(l.Name, isCfg)
			}
		}
	}
}

func recordPollLoop(fset *token.FileSet, loop ast.Node, scope *pathScope, live *liveFuncAliases, fn *ast.FuncDecl, filename string, aliasToPath map[string]string, aliases funcValueAliases, idx *configPathPkgIndex, timerVals, timerChans map[string]bool, pkgShadows map[string]bool, ctx *pollPkgContext, pkgPath string, out *[]string, loopLabels ...string) {
	repeatable := loopIsRepeatable(loop, timerVals, timerChans, aliasToPath, aliases)
	timerDriven := loopConsumesOrResetsTimer(loop, timerVals, timerChans, aliasToPath, aliases)
	if !repeatable && !timerDriven {
		return
	}
	if timerDriven && !repeatable {
		if rs, ok := loop.(*ast.RangeStmt); ok && isTimerChannelExpr(rs.X, timerVals, timerChans, aliasToPath, aliases) {
			repeatable = true
		} else {
			return
		}
	}
	if !repeatable {
		return
	}
	var body *ast.BlockStmt
	var forPost ast.Stmt
	switch l := loop.(type) {
	case *ast.ForStmt:
		body = l.Body
		forPost = l.Post
	case *ast.RangeStmt:
		body = l.Body
	}
	if body == nil {
		return
	}

	labelSet := map[string]bool{}
	for _, lb := range loopLabels {
		labelSet[lb] = true
	}
	bodyLabels := indexLabelsIn(body)

	entry, _ := forkPathEnv(scope)
	if fs, ok := loop.(*ast.ForStmt); ok && fs.Init != nil {
		if assign, ok := fs.Init.(*ast.AssignStmt); ok {
			analyzePathAssign(assign, entry, idx, fn)
		} else if ds, ok := fs.Init.(*ast.DeclStmt); ok {
			if gen, ok := ds.Decl.(*ast.GenDecl); ok {
				analyzePathGenDecl(gen, entry, idx, fn)
			}
		}
	}
	entryIDs := pathRelevantEntryIDs(entry, body, aliasToPath, aliases)
	nTrack := len(entryIDs)
	maxStates := 1
	if nTrack > 0 {
		if nTrack > 12 {
			*out = append(*out, formatPos(fset, loop.Pos())+": path abstract-state explosion (>12 decls)")
			return
		}
		maxStates = 1 << nTrack
	}

	graph := map[string]*pollLoopStateNode{}
	startKey := absPathStateKey(entry, entryIDs)
	worklist := []string{startKey}
	graph[startKey] = &pollLoopStateNode{scope: entry}

	for len(worklist) > 0 {
		if len(graph) > maxStates {
			*out = append(*out, formatPos(fset, loop.Pos())+": path abstract-state explosion")
			return
		}
		key := worklist[0]
		worklist = worklist[1:]
		node := graph[key]
		sim := clonePathScopeTree(node.scope)
		succs, err := transitionPollBodyStates(body, sim, live, entryIDs, maxStates, fn, filename, aliasToPath, aliases, idx, labelSet, bodyLabels, pkgShadows, ctx, pkgPath)
		if err != nil {
			*out = append(*out, formatPos(fset, loop.Pos())+": "+err.Error())
			return
		}
		var nextScopes []*pathScope
		backedgeHadConfig := false
		for _, succ := range succs {
			switch succ.kind {
			case pollFlowBackedge, pollFlowContinue:
				if succ.hadConfig {
					backedgeHadConfig = true
				}
				var cont *pathScope
				if forPost != nil {
					posted := clonePathScopeTree(succ.scope)
					applyPathPostStmt(forPost, posted, idx, fn)
					cont = snapshotPathEnvByID(posted, entryIDs)
				} else {
					cont = snapshotPathEnvByID(succ.scope, entryIDs)
				}
				nextScopes = append(nextScopes, cont)
			case pollFlowUnresolved:
				// Unknown target: do not prove at-most-once (already), and only
				// a config-carrying unresolved edge can establish polling — without
				// tainting sibling break/return paths.
				if succ.hadConfig {
					backedgeHadConfig = true
					cont := snapshotPathEnvByID(succ.scope, entryIDs)
					nextScopes = append(nextScopes, cont)
				}
			case pollFlowBreak, pollFlowTerminal:
				// Transient path — not a recurrent successor.
			}
		}
		node.hadConfig = backedgeHadConfig
		seenExit := map[string]bool{}
		for _, ns := range nextScopes {
			ek := absPathStateKey(ns, entryIDs)
			if seenExit[ek] {
				continue
			}
			seenExit[ek] = true
			node.exits = append(node.exits, ek)
			if _, ok := graph[ek]; !ok {
				graph[ek] = &pollLoopStateNode{scope: ns}
				worklist = append(worklist, ek)
			}
		}
	}

	recurrent := recurrentPollLoopStates(graph)
	hasRecurrentConfig := false
	for key, node := range graph {
		if recurrent[key] && node.hadConfig {
			hasRecurrentConfig = true
			_ = recordPollProbesOrdered(fset, body, clonePathScopeTree(node.scope), live, fn, filename, aliasToPath, aliases, idx, ctx, pkgPath, out)
			break
		}
	}
	if !hasRecurrentConfig {
		return
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if tname, ok := timeCallName(call, aliasToPath, aliases); ok && tname != "AfterFunc" {
			*out = append(*out, formatPos(fset, call.Pos())+": time."+tname+" config-reload poller")
		}
		return true
	})
	if timerDriven {
		*out = append(*out, formatPos(fset, loop.Pos())+": timer/ticker channel poll loop")
	}
}

type pollLoopStateNode struct {
	scope     *pathScope
	hadConfig bool // config evidence on a backedge/continue successor from this state
	exits     []string
}

// pollFlowKind classifies a loop-body path successor.
type pollFlowKind int

const (
	pollFlowFallthrough pollFlowKind = iota // continue to next statement in block
	pollFlowBackedge                        // fallthrough to next iteration
	pollFlowContinue                        // continue current loop
	pollFlowBreak                           // break current loop
	pollFlowTerminal                        // return / panic / goto outside
	pollFlowUnresolved                      // unknown labeled target
)

// pollPathSucc is one path-specific successor of a loop-body transition.
type pollPathSucc struct {
	scope     *pathScope
	hadConfig bool
	aliases   *liveFuncAliases
	kind      pollFlowKind
	targetID  int // break/continue target identity; 0 = analyzed loop
}

func recurrentPollLoopStates(graph map[string]*pollLoopStateNode) map[string]bool {
	recurrent := map[string]bool{}
	for start := range graph {
		if pollStateCanReachSelf(graph, start) {
			recurrent[start] = true
		}
	}
	return recurrent
}

func pollStateCanReachSelf(graph map[string]*pollLoopStateNode, start string) bool {
	node := graph[start]
	if node == nil {
		return false
	}
	stack := append([]string{}, node.exits...)
	seen := map[string]bool{}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == start {
			return true
		}
		if seen[cur] {
			continue
		}
		seen[cur] = true
		n := graph[cur]
		if n == nil {
			continue
		}
		stack = append(stack, n.exits...)
	}
	return false
}

func applyPathPostStmt(stmt ast.Stmt, scope *pathScope, idx *configPathPkgIndex, fn *ast.FuncDecl) {
	if stmt == nil || scope == nil {
		return
	}
	if assign, ok := stmt.(*ast.AssignStmt); ok {
		analyzePathAssign(assign, scope, idx, fn)
	}
}

func clonePathScopeTree(scope *pathScope) *pathScope {
	if scope == nil {
		return nil
	}
	var frames []*pathScope
	for cur := scope; cur != nil; cur = cur.parent {
		frames = append(frames, cur)
	}
	var parent *pathScope
	for i := len(frames) - 1; i >= 0; i-- {
		n := newPathScope(parent)
		names := make([]string, 0, len(frames[i].decls))
		for name := range frames[i].decls {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			d := frames[i].decls[name]
			n.decls[name] = &pathDecl{isConfig: d.isConfig, id: d.id}
		}
		parent = n
	}
	return parent
}

// transitionPollBodyStates explores one loop-body execution from entry.
// Each successor carries its own abstract path state, hadConfig evidence, and
// flow outcome. Config bits are never OR-merged across sibling branches.
func transitionPollBodyStates(block *ast.BlockStmt, entry *pathScope, liveEntry *liveFuncAliases, entryIDs map[string]int, maxStates int, fn *ast.FuncDecl, filename string, aliasToPath map[string]string, aliases funcValueAliases, idx *configPathPkgIndex, loopLabels map[string]bool, bodyLabels map[string]*ast.LabeledStmt, pkgShadows map[string]bool, ctx *pollPkgContext, pkgPath string) ([]pollPathSucc, error) {
	if block == nil {
		return []pollPathSucc{{scope: entry, kind: pollFlowBackedge}}, nil
	}
	if loopLabels == nil {
		loopLabels = map[string]bool{}
	}
	_ = bodyLabels // retained for callers; nested resolution uses path indexes below.

	type pathState struct {
		scope     *pathScope
		hadConfig bool
		lex       *nameShadowScope
		aliases   *liveFuncAliases
	}

	idsFor := func(scope *pathScope) map[string]int {
		if len(entryIDs) > 0 {
			return entryIDs
		}
		return visiblePathEntryIDs(scope)
	}

	bodyLabelSites := indexLabelsWithPath(block.List)
	var fnLabelSites map[string]labelSite
	if fn != nil && fn.Body != nil {
		fnLabelSites = indexLabelsWithPath(fn.Body.List)
	} else {
		fnLabelSites = bodyLabelSites
	}

	analyzed := &ctrlTarget{id: analyzedLoopTargetID, kind: ctrlKindFor, labels: map[string]bool{}}
	for lb := range loopLabels {
		analyzed.labels[lb] = true
	}
	var ctrlStack []*ctrlTarget
	nextCtrlID := 1

	pushCtrl := func(kind ctrlTargetKind, labels []string) *ctrlTarget {
		t := &ctrlTarget{id: nextCtrlID, kind: kind, labels: map[string]bool{}}
		nextCtrlID++
		for _, lb := range labels {
			if lb != "" {
				t.labels[lb] = true
			}
		}
		ctrlStack = append(ctrlStack, t)
		return t
	}
	popCtrl := func() {
		if len(ctrlStack) > 0 {
			ctrlStack = ctrlStack[:len(ctrlStack)-1]
		}
	}

	resolveBreakTarget := func(br *ast.BranchStmt) (*ctrlTarget, pollFlowKind) {
		if br.Label == nil {
			if len(ctrlStack) > 0 {
				t := ctrlStack[len(ctrlStack)-1]
				if t.isBreakFallthrough() {
					return t, pollFlowBreak
				}
				// Nested loop break: exit only that loop (resume after it).
				return t, pollFlowBreak
			}
			return analyzed, pollFlowBreak
		}
		name := br.Label.Name
		for i := len(ctrlStack) - 1; i >= 0; i-- {
			if ctrlStack[i].labels[name] {
				return ctrlStack[i], pollFlowBreak
			}
		}
		if analyzed.labels[name] {
			return analyzed, pollFlowBreak
		}
		// Unknown/invalid target: do not claim analyzed-loop exit.
		return nil, pollFlowUnresolved
	}

	resolveContinueKind := func(br *ast.BranchStmt) (pollFlowKind, int) {
		if br.Label == nil {
			for i := len(ctrlStack) - 1; i >= 0; i-- {
				if ctrlStack[i].isLoop() {
					// Continue nested loop — opaque / unresolved for outer analysis.
					return pollFlowUnresolved, ctrlStack[i].id
				}
			}
			return pollFlowContinue, analyzedLoopTargetID
		}
		name := br.Label.Name
		if analyzed.labels[name] {
			return pollFlowContinue, analyzedLoopTargetID
		}
		for i := len(ctrlStack) - 1; i >= 0; i-- {
			if ctrlStack[i].labels[name] {
				if ctrlStack[i].isLoop() {
					return pollFlowUnresolved, ctrlStack[i].id
				}
				// continue to non-loop label is invalid — unresolved.
				return pollFlowUnresolved, 0
			}
		}
		return pollFlowUnresolved, 0
	}

	var (
		walkExpr       func(ast.Expr, *pathState)
		walkStmt       func(ast.Stmt, pathState) ([]pollPathSucc, error)
		walkStmtList   func([]ast.Stmt, int, pathState, int, pollFlowKind) ([]pollPathSucc, error)
		walkBlockSeq   func(*ast.BlockStmt, pathState) ([]pollPathSucc, error)
		remapCtrlBreak func([]pollPathSucc, *ctrlTarget) []pollPathSucc
		gotoResume     func(labelSite, pathState, int, pollFlowKind) ([]pollPathSucc, error)
	)

	probeConfig := func(call *ast.CallExpr, st *pathState) {
		liveMap := aliases
		if st.aliases != nil {
			liveMap = st.aliases.flat()
		}
		if callHasConfigSourceEvidence(call, filename, aliasToPath, liveMap, idx, st.scope, fn) {
			st.hadConfig = true
		}
		if ctx != nil {
			if id := resolveInPackageCallTarget(call, fn, pkgPath, ctx); id != "" && ctx.reachesConfig[id] {
				st.hadConfig = true
			}
		}
	}

	walkExpr = func(expr ast.Expr, st *pathState) {
		if expr == nil {
			return
		}
		switch e := unwrapParenExpr(expr).(type) {
		case *ast.CallExpr:
			liveMap := aliases
			if st.aliases != nil {
				liveMap = st.aliases.flat()
			}
			if _, ok := osProbeCallName(e, aliasToPath, liveMap); ok {
				probeConfig(e, st)
			} else {
				probeConfig(e, st) // transitive helper / alias resolution
			}
			walkExpr(e.Fun, st)
			for _, arg := range e.Args {
				walkExpr(arg, st)
			}
		case *ast.BinaryExpr:
			walkExpr(e.X, st)
			walkExpr(e.Y, st)
		case *ast.UnaryExpr:
			walkExpr(e.X, st)
		case *ast.SelectorExpr:
			walkExpr(e.X, st)
		case *ast.IndexExpr:
			walkExpr(e.X, st)
			walkExpr(e.Index, st)
		case *ast.SliceExpr:
			walkExpr(e.X, st)
			walkExpr(e.Low, st)
			walkExpr(e.High, st)
			walkExpr(e.Max, st)
		case *ast.StarExpr:
			walkExpr(e.X, st)
		case *ast.ParenExpr:
			walkExpr(e.X, st)
		case *ast.CompositeLit:
			for _, elt := range e.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					walkExpr(kv.Value, st)
				} else {
					walkExpr(elt, st)
				}
			}
		}
	}

	remapCtrlBreak = func(outs []pollPathSucc, tgt *ctrlTarget) []pollPathSucc {
		if tgt == nil {
			return outs
		}
		for i := range outs {
			if outs[i].kind == pollFlowBreak && outs[i].targetID == tgt.id {
				if tgt.isBreakFallthrough() {
					outs[i].kind = pollFlowFallthrough
					outs[i].targetID = 0
				} else if tgt.isLoop() {
					// Nested loop break resumes after the nested loop.
					outs[i].kind = pollFlowFallthrough
					outs[i].targetID = 0
				}
			}
		}
		return outs
	}

	walkStmtList = func(stmts []ast.Stmt, from int, st pathState, depth int, endKind pollFlowKind) ([]pollPathSucc, error) {
		if depth > 64 {
			return nil, fmt.Errorf("path abstract-state explosion (goto depth)")
		}
		cur := []pathState{st}
		var finished []pollPathSucc
		for i := from; i < len(stmts); i++ {
			var next []pathState
			for _, ps := range cur {
				outs, err := walkStmt(stmts[i], ps)
				if err != nil {
					return nil, err
				}
				for _, o := range outs {
					switch o.kind {
					case pollFlowFallthrough:
						next = append(next, pathState{scope: o.scope, hadConfig: o.hadConfig, lex: ps.lex, aliases: o.aliases})
					default:
						finished = append(finished, o)
					}
				}
			}
			if len(next) == 0 {
				return finished, nil
			}
			as := make([]pollPathState, len(next))
			for i := range next {
				as[i] = pollPathState{scope: next[i].scope, hadConfig: next[i].hadConfig}
			}
			deduped, err := dedupePathStates(as, idsFor(st.scope), maxStates)
			if err != nil {
				return nil, err
			}
			cur = cur[:0]
			for _, d := range deduped {
				// Preserve lex from any next state with matching scope key; lex is
				// shared for the statement-list frame.
				lex := st.lex
				if len(next) > 0 {
					lex = next[0].lex
				}
				// Alias env is statement-order lexical; preserve the frame's live aliases.
				als := st.aliases
				if len(next) > 0 && next[0].aliases != nil {
					als = next[0].aliases
				}
				cur = append(cur, pathState{scope: d.scope, hadConfig: d.hadConfig, lex: lex, aliases: als})
			}
		}
		for _, ps := range cur {
			finished = append(finished, pollPathSucc{scope: ps.scope, hadConfig: ps.hadConfig, aliases: ps.aliases, kind: endKind})
		}
		return finished, nil
	}

	gotoResume = func(site labelSite, st pathState, depth int, endKind pollFlowKind) ([]pollPathSucc, error) {
		if len(site.path) == 0 {
			return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowUnresolved}}, nil
		}
		var runFrom func(frameIdx, fromIndex int, st pathState) ([]pollPathSucc, error)
		runFrom = func(frameIdx, fromIndex int, st pathState) ([]pollPathSucc, error) {
			frame := site.path[frameIdx]
			localEnd := pollFlowFallthrough
			if frameIdx == 0 {
				localEnd = endKind
			}
			outs, err := walkStmtList(frame.list, fromIndex, st, depth+1, localEnd)
			if err != nil {
				return nil, err
			}
			if frameIdx == 0 {
				return outs, nil
			}
			var result []pollPathSucc
			for _, o := range outs {
				if o.kind != pollFlowFallthrough {
					result = append(result, o)
					continue
				}
				parent := site.path[frameIdx-1]
				more, err := runFrom(frameIdx-1, parent.index+1, pathState{scope: o.scope, hadConfig: o.hadConfig, lex: st.lex, aliases: o.aliases})
				if err != nil {
					return nil, err
				}
				result = append(result, more...)
			}
			return result, nil
		}
		deep := len(site.path) - 1
		return runFrom(deep, site.path[deep].index, st)
	}

	walkStmt = func(stmt ast.Stmt, st pathState) ([]pollPathSucc, error) {
		if stmt == nil {
			return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowFallthrough}}, nil
		}
		labels, core := peelLabeledStmt(stmt)
		switch s := core.(type) {
		case *ast.DeclStmt:
			if gen, ok := s.Decl.(*ast.GenDecl); ok {
				analyzePathGenDecl(gen, st.scope, idx, fn)
				declareGenDeclShadowNames(st.lex, gen)
				if st.aliases == nil {
					st.aliases = newLiveFuncAliases(nil)
				}
				for _, spec := range gen.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						applyLiveFuncAliasValueSpec(vs, st.aliases, aliasToPath, pkgPath)
					}
				}
			}
			return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: st.aliases, kind: pollFlowFallthrough}}, nil
		case *ast.AssignStmt:
			for _, rhs := range s.Rhs {
				walkExpr(rhs, &st)
			}
			analyzePathAssign(s, st.scope, idx, fn)
			declareAssignDefineShadowNames(st.lex, s)
			if st.aliases == nil {
				st.aliases = newLiveFuncAliases(nil)
			}
			if s.Tok == token.DEFINE {
				st.aliases = newLiveFuncAliases(st.aliases)
			}
			applyLiveFuncAliasAssign(s, st.aliases, aliasToPath, pkgPath)
			return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: st.aliases, kind: pollFlowFallthrough}}, nil
		case *ast.ExprStmt:
			walkExpr(s.X, &st)
			if isBuiltinPanicCall(s.X, st.lex) {
				return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowTerminal}}, nil
			}
			return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowFallthrough}}, nil
		case *ast.IncDecStmt:
			walkExpr(s.X, &st)
			return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowFallthrough}}, nil
		case *ast.ReturnStmt:
			for _, r := range s.Results {
				walkExpr(r, &st)
			}
			return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowTerminal}}, nil
		case *ast.BranchStmt:
			switch s.Tok {
			case token.BREAK:
				tgt, kind := resolveBreakTarget(s)
				id := analyzedLoopTargetID
				if tgt != nil {
					id = tgt.id
				}
				return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: kind, targetID: id}}, nil
			case token.CONTINUE:
				kind, id := resolveContinueKind(s)
				return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: kind, targetID: id}}, nil
			case token.GOTO:
				if s.Label == nil {
					return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowUnresolved}}, nil
				}
				name := s.Label.Name
				site, inFn := fnLabelSites[name]
				if !inFn {
					return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowUnresolved}}, nil
				}
				bodySite, inBody := bodyLabelSites[name]
				if !inBody || site.labeled == nil || !labeledStmtInside(site.labeled, block) {
					return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowTerminal}}, nil
				}
				// Prefer body-rooted path for resume.
				resume := bodySite
				if resume.labeled == nil {
					resume = site
				}
				if resume.labeled != nil && resume.labeled.Pos() <= s.Pos() {
					return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowContinue}}, nil
				}
				return gotoResume(resume, st, 0, pollFlowBackedge)
			default:
				return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowFallthrough}}, nil
			}
		case *ast.BlockStmt:
			child := newPathScope(st.scope)
			lexChild := newNameShadowScope(st.lex)
			childSt := pathState{scope: child, hadConfig: st.hadConfig, lex: lexChild, aliases: newLiveFuncAliases(st.aliases)}
			outs, err := walkBlockSeq(s, childSt)
			if err != nil {
				return nil, err
			}
			parentIDs := idsFor(st.scope)
			var conts []pollPathSucc
			for _, o := range outs {
				snap := snapshotPathEnvByID(o.scope, parentIDs)
				conts = append(conts, pollPathSucc{scope: snap, hadConfig: o.hadConfig, aliases: o.aliases, kind: o.kind, targetID: o.targetID})
			}
			return conts, nil
		case *ast.IfStmt:
			parentIDs := idsFor(st.scope)
			ifScope := newPathScope(st.scope)
			lexChild := newNameShadowScope(st.lex)
			ifSt := pathState{scope: ifScope, hadConfig: st.hadConfig, lex: lexChild, aliases: st.aliases}
			if s.Init != nil {
				initOuts, err := walkStmt(s.Init, ifSt)
				if err != nil {
					return nil, err
				}
				for _, o := range initOuts {
					if o.kind == pollFlowFallthrough || o.kind == pollFlowBackedge {
						ifSt.scope = o.scope
						ifSt.hadConfig = o.hadConfig
					}
				}
			}
			walkExpr(s.Cond, &ifSt)
			thenEnv := clonePathScopeTree(ifSt.scope)
			thenAlias, _ := forkLiveFuncAliases(ifSt.aliases)
			thenOuts, err := walkBlockSeq(s.Body, pathState{scope: thenEnv, hadConfig: ifSt.hadConfig, lex: newNameShadowScope(ifSt.lex), aliases: thenAlias})
			if err != nil {
				return nil, err
			}
			var elseOuts []pollPathSucc
			if s.Else == nil {
				elseOuts = []pollPathSucc{{scope: clonePathScopeTree(ifSt.scope), hadConfig: ifSt.hadConfig, aliases: ifSt.aliases, kind: pollFlowFallthrough}}
			} else {
				elseEnv := clonePathScopeTree(ifSt.scope)
				elseAlias, _ := forkLiveFuncAliases(ifSt.aliases)
				elseOuts, err = walkStmt(s.Else, pathState{scope: elseEnv, hadConfig: ifSt.hadConfig, lex: newNameShadowScope(ifSt.lex), aliases: elseAlias})
				if err != nil {
					return nil, err
				}
			}
			var conts []pollPathSucc
			for _, o := range thenOuts {
				conts = append(conts, pollPathSucc{scope: snapshotPathEnvByID(o.scope, parentIDs), hadConfig: o.hadConfig, aliases: o.aliases, kind: o.kind, targetID: o.targetID})
			}
			for _, o := range elseOuts {
				conts = append(conts, pollPathSucc{scope: snapshotPathEnvByID(o.scope, parentIDs), hadConfig: o.hadConfig, aliases: o.aliases, kind: o.kind, targetID: o.targetID})
			}
			return conts, nil
		case *ast.SwitchStmt:
			tgt := pushCtrl(ctrlKindSwitch, labels)
			defer popCtrl()
			parentIDs := idsFor(st.scope)
			swScope := newPathScope(st.scope)
			lexChild := newNameShadowScope(st.lex)
			swSt := pathState{scope: swScope, hadConfig: st.hadConfig, lex: lexChild, aliases: st.aliases}
			if s.Init != nil {
				if _, err := walkStmt(s.Init, swSt); err != nil {
					return nil, err
				}
			}
			walkExpr(s.Tag, &swSt)
			var conts []pollPathSucc
			hasDefault := false
			if s.Body != nil {
				for _, clause := range s.Body.List {
					cc, ok := clause.(*ast.CaseClause)
					if !ok {
						continue
					}
					if cc.List == nil {
						hasDefault = true
					}
					br := clonePathScopeTree(swSt.scope)
					brSt := pathState{scope: br, hadConfig: swSt.hadConfig, lex: newNameShadowScope(swSt.lex), aliases: newLiveFuncAliases(swSt.aliases)}
					for _, e := range cc.List {
						walkExpr(e, &brSt)
					}
					outs, err := walkBlockSeq(&ast.BlockStmt{List: cc.Body}, brSt)
					if err != nil {
						return nil, err
					}
					outs = remapCtrlBreak(outs, tgt)
					for _, o := range outs {
						conts = append(conts, pollPathSucc{scope: snapshotPathEnvByID(o.scope, parentIDs), hadConfig: o.hadConfig, aliases: o.aliases, kind: o.kind, targetID: o.targetID})
					}
				}
			}
			if !hasDefault {
				conts = append(conts, pollPathSucc{scope: snapshotPathEnvByID(swSt.scope, parentIDs), hadConfig: swSt.hadConfig, aliases: swSt.aliases, kind: pollFlowFallthrough})
			}
			if len(conts) == 0 {
				conts = []pollPathSucc{{scope: snapshotPathEnvByID(swSt.scope, parentIDs), hadConfig: swSt.hadConfig, aliases: nil, kind: pollFlowFallthrough}}
			}
			return conts, nil
		case *ast.TypeSwitchStmt:
			tgt := pushCtrl(ctrlKindTypeSwitch, labels)
			defer popCtrl()
			parentIDs := idsFor(st.scope)
			swScope := newPathScope(st.scope)
			lexChild := newNameShadowScope(st.lex)
			swSt := pathState{scope: swScope, hadConfig: st.hadConfig, lex: lexChild, aliases: st.aliases}
			if s.Init != nil {
				if _, err := walkStmt(s.Init, swSt); err != nil {
					return nil, err
				}
			}
			if s.Assign != nil {
				if _, err := walkStmt(s.Assign, swSt); err != nil {
					return nil, err
				}
			}
			var conts []pollPathSucc
			hasDefault := false
			if s.Body != nil {
				for _, clause := range s.Body.List {
					cc, ok := clause.(*ast.CaseClause)
					if !ok {
						continue
					}
					if cc.List == nil {
						hasDefault = true
					}
					br := clonePathScopeTree(swSt.scope)
					outs, err := walkBlockSeq(&ast.BlockStmt{List: cc.Body}, pathState{scope: br, hadConfig: swSt.hadConfig, lex: newNameShadowScope(swSt.lex), aliases: newLiveFuncAliases(swSt.aliases)})
					if err != nil {
						return nil, err
					}
					outs = remapCtrlBreak(outs, tgt)
					for _, o := range outs {
						conts = append(conts, pollPathSucc{scope: snapshotPathEnvByID(o.scope, parentIDs), hadConfig: o.hadConfig, aliases: o.aliases, kind: o.kind, targetID: o.targetID})
					}
				}
			}
			if !hasDefault {
				conts = append(conts, pollPathSucc{scope: snapshotPathEnvByID(swSt.scope, parentIDs), hadConfig: swSt.hadConfig, aliases: swSt.aliases, kind: pollFlowFallthrough})
			}
			if len(conts) == 0 {
				conts = []pollPathSucc{{scope: snapshotPathEnvByID(swSt.scope, parentIDs), hadConfig: swSt.hadConfig, aliases: nil, kind: pollFlowFallthrough}}
			}
			return conts, nil
		case *ast.SelectStmt:
			tgt := pushCtrl(ctrlKindSelect, labels)
			defer popCtrl()
			parentIDs := idsFor(st.scope)
			var conts []pollPathSucc
			hasDefault := false
			if s.Body != nil {
				for _, clause := range s.Body.List {
					cc, ok := clause.(*ast.CommClause)
					if !ok {
						continue
					}
					if cc.Comm == nil {
						hasDefault = true
					}
					br := clonePathScopeTree(st.scope)
					brSt := pathState{scope: br, hadConfig: st.hadConfig, lex: newNameShadowScope(st.lex), aliases: newLiveFuncAliases(st.aliases)}
					if cc.Comm != nil {
						if _, err := walkStmt(cc.Comm, brSt); err != nil {
							return nil, err
						}
					}
					outs, err := walkBlockSeq(&ast.BlockStmt{List: cc.Body}, brSt)
					if err != nil {
						return nil, err
					}
					outs = remapCtrlBreak(outs, tgt)
					for _, o := range outs {
						conts = append(conts, pollPathSucc{scope: snapshotPathEnvByID(o.scope, parentIDs), hadConfig: o.hadConfig, aliases: o.aliases, kind: o.kind, targetID: o.targetID})
					}
				}
			}
			if !hasDefault {
				conts = append(conts, pollPathSucc{scope: snapshotPathEnvByID(st.scope, parentIDs), hadConfig: st.hadConfig, aliases: st.aliases, kind: pollFlowFallthrough})
			}
			if len(conts) == 0 {
				conts = []pollPathSucc{{scope: snapshotPathEnvByID(st.scope, parentIDs), hadConfig: st.hadConfig, aliases: nil, kind: pollFlowFallthrough}}
			}
			return conts, nil
		case *ast.ForStmt, *ast.RangeStmt:
			// Nested loops remain opaque for probe attribution; analyzed via their
			// own recordPollLoop. Break/continue inside are not visible here.
			return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowFallthrough}}, nil
		case *ast.GoStmt:
			walkExpr(s.Call, &st)
			return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowFallthrough}}, nil
		case *ast.DeferStmt:
			walkExpr(s.Call, &st)
			return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowFallthrough}}, nil
		case *ast.SendStmt:
			walkExpr(s.Chan, &st)
			walkExpr(s.Value, &st)
			return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowFallthrough}}, nil
		default:
			return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowFallthrough}}, nil
		}
	}

	walkBlockSeq = func(b *ast.BlockStmt, st pathState) ([]pollPathSucc, error) {
		if b == nil {
			return []pollPathSucc{{scope: st.scope, hadConfig: st.hadConfig, aliases: nil, kind: pollFlowFallthrough}}, nil
		}
		return walkStmtList(b.List, 0, st, 0, pollFlowFallthrough)
	}

	entryLex := seedNameShadowsAtLoop(fn, block, pkgShadows)
	entryAliases := liveEntry
	if entryAliases == nil {
		entryAliases = newLiveFuncAliases(nil)
	}
	return walkStmtList(block.List, 0, pathState{scope: entry, hadConfig: false, lex: entryLex, aliases: entryAliases.cloneTree()}, 0, pollFlowBackedge)
}

func stmtContainsNode(root ast.Stmt, target ast.Node) bool {
	if root == nil || target == nil {
		return false
	}
	found := false
	ast.Inspect(root, func(n ast.Node) bool {
		if n == target {
			found = true
			return false
		}
		return !found
	})
	return found
}

type pollPathState struct {
	scope     *pathScope
	hadConfig bool
}

func dedupePathStates(states []pollPathState, ids map[string]int, maxStates int) ([]pollPathState, error) {
	if len(states) == 0 {
		return states, nil
	}
	seen := map[string]bool{}
	out := make([]pollPathState, 0, len(states))
	for _, st := range states {
		if st.scope == nil {
			continue
		}
		key := absPathStateKey(st.scope, ids)
		if st.hadConfig {
			key += "#1"
		} else {
			key += "#0"
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, st)
	}
	if maxStates > 0 && len(out) > maxStates*2 {
		return nil, fmt.Errorf("path abstract-state explosion (%d > %d)", len(out), maxStates*2)
	}
	return out, nil
}

// recordPollProbesOrdered walks stmts in execution order, updating pathScope and
// classifying each os probe with the state reaching that program point.
// Nested loops are skipped (recorded separately). Returns whether any config
// probe was recorded.
func recordPollProbesOrdered(fset *token.FileSet, block *ast.BlockStmt, scope *pathScope, live *liveFuncAliases, fn *ast.FuncDecl, filename string, aliasToPath map[string]string, aliases funcValueAliases, idx *configPathPkgIndex, ctx *pollPkgContext, pkgPath string, out *[]string) bool {
	if block == nil {
		return false
	}
	found := false
	var walkBlock func(*ast.BlockStmt, *pathScope)
	var walkStmt func(ast.Stmt, *pathScope)
	var walkExpr func(ast.Expr, *pathScope)

	recordProbe := func(call *ast.CallExpr, scope *pathScope) {
		liveMap := aliases
		if live != nil {
			liveMap = live.flat()
		}
		if callHasConfigSourceEvidence(call, filename, aliasToPath, liveMap, idx, scope, fn) {
			found = true
			pname, _ := osProbeCallName(call, aliasToPath, liveMap)
			label := "os." + pname
			if _, aliased := callViaFuncAlias(call, liveMap); aliased {
				label += " (func-value alias)"
			}
			*out = append(*out, formatPos(fset, call.Pos())+": "+label+" config-source probe")
			return
		}
		if ctx != nil {
			if id := resolveInPackageCallTarget(call, fn, pkgPath, ctx); id != "" && ctx.reachesConfig[id] {
				found = true
				*out = append(*out, formatPos(fset, call.Pos())+": transitive config-source probe via "+id)
			}
		}
	}

	walkExpr = func(expr ast.Expr, scope *pathScope) {
		if expr == nil {
			return
		}
		switch e := unwrapParenExpr(expr).(type) {
		case *ast.CallExpr:
			liveMap := aliases
			if live != nil {
				liveMap = live.flat()
			}
			if _, ok := osProbeCallName(e, aliasToPath, liveMap); ok {
				recordProbe(e, scope)
			} else {
				recordProbe(e, scope)
			}
			walkExpr(e.Fun, scope)
			for _, arg := range e.Args {
				walkExpr(arg, scope)
			}
		case *ast.BinaryExpr:
			walkExpr(e.X, scope)
			walkExpr(e.Y, scope)
		case *ast.UnaryExpr:
			walkExpr(e.X, scope)
		case *ast.SelectorExpr:
			walkExpr(e.X, scope)
		case *ast.IndexExpr:
			walkExpr(e.X, scope)
			walkExpr(e.Index, scope)
		case *ast.SliceExpr:
			walkExpr(e.X, scope)
			walkExpr(e.Low, scope)
			walkExpr(e.High, scope)
			walkExpr(e.Max, scope)
		case *ast.StarExpr:
			walkExpr(e.X, scope)
		case *ast.ParenExpr:
			walkExpr(e.X, scope)
		case *ast.CompositeLit:
			for _, elt := range e.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					walkExpr(kv.Value, scope)
				} else {
					walkExpr(elt, scope)
				}
			}
		case *ast.FuncLit:
			// Nested func literals are not loop-body probes for this iteration.
		}
	}

	walkStmt = func(stmt ast.Stmt, scope *pathScope) {
		if stmt == nil {
			return
		}
		switch s := stmt.(type) {
		case *ast.DeclStmt:
			if gen, ok := s.Decl.(*ast.GenDecl); ok {
				analyzePathGenDecl(gen, scope, idx, fn)
			}
		case *ast.AssignStmt:
			for _, rhs := range s.Rhs {
				walkExpr(rhs, scope)
			}
			analyzePathAssign(s, scope, idx, fn)
		case *ast.ExprStmt:
			walkExpr(s.X, scope)
		case *ast.IncDecStmt:
			walkExpr(s.X, scope)
		case *ast.ReturnStmt:
			for _, r := range s.Results {
				walkExpr(r, scope)
			}
		case *ast.BlockStmt:
			child := newPathScope(scope)
			walkBlock(s, child)
		case *ast.IfStmt:
			ifScope := newPathScope(scope)
			if s.Init != nil {
				walkStmt(s.Init, ifScope)
			}
			walkExpr(s.Cond, ifScope)
			_, preexisting := forkPathEnv(ifScope)
			thenEnv, _ := forkPathEnv(ifScope)
			walkBlock(s.Body, thenEnv)
			var elseEnv *pathScope
			if s.Else == nil {
				elseEnv = snapshotPathEnv(ifScope, preexisting)
			} else {
				elseEnv, _ = forkPathEnv(ifScope)
				walkStmt(s.Else, elseEnv)
			}
			mergePathEnvs(ifScope, preexisting, thenEnv, elseEnv)
		case *ast.ForStmt, *ast.RangeStmt:
			// Nested loops are recorded by their own analyzePollStmt visit.
			return
		case *ast.SwitchStmt:
			swScope := newPathScope(scope)
			if s.Init != nil {
				walkStmt(s.Init, swScope)
			}
			walkExpr(s.Tag, swScope)
			_, preexisting := forkPathEnv(swScope)
			var branches []*pathScope
			hasDefault := false
			if s.Body != nil {
				for _, clause := range s.Body.List {
					cc, ok := clause.(*ast.CaseClause)
					if !ok {
						continue
					}
					if cc.List == nil {
						hasDefault = true
					}
					br, _ := forkPathEnv(swScope)
					for _, e := range cc.List {
						walkExpr(e, br)
					}
					walkBlock(&ast.BlockStmt{List: cc.Body}, br)
					branches = append(branches, br)
				}
			}
			if !hasDefault {
				branches = append(branches, snapshotPathEnv(swScope, preexisting))
			}
			if len(branches) > 0 {
				mergePathEnvs(swScope, preexisting, branches...)
			}
		case *ast.TypeSwitchStmt:
			swScope := newPathScope(scope)
			if s.Init != nil {
				walkStmt(s.Init, swScope)
			}
			if s.Assign != nil {
				walkStmt(s.Assign, swScope)
			}
			_, preexisting := forkPathEnv(swScope)
			var branches []*pathScope
			hasDefault := false
			if s.Body != nil {
				for _, clause := range s.Body.List {
					cc, ok := clause.(*ast.CaseClause)
					if !ok {
						continue
					}
					if cc.List == nil {
						hasDefault = true
					}
					br, _ := forkPathEnv(swScope)
					walkBlock(&ast.BlockStmt{List: cc.Body}, br)
					branches = append(branches, br)
				}
			}
			if !hasDefault {
				branches = append(branches, snapshotPathEnv(swScope, preexisting))
			}
			if len(branches) > 0 {
				mergePathEnvs(swScope, preexisting, branches...)
			}
		case *ast.SelectStmt:
			_, preexisting := forkPathEnv(scope)
			var branches []*pathScope
			hasDefault := false
			if s.Body != nil {
				for _, clause := range s.Body.List {
					cc, ok := clause.(*ast.CommClause)
					if !ok {
						continue
					}
					if cc.Comm == nil {
						hasDefault = true
					}
					br, _ := forkPathEnv(scope)
					if cc.Comm != nil {
						walkStmt(cc.Comm, br)
					}
					walkBlock(&ast.BlockStmt{List: cc.Body}, br)
					branches = append(branches, br)
				}
			}
			if !hasDefault {
				branches = append(branches, snapshotPathEnv(scope, preexisting))
			}
			if len(branches) > 0 {
				mergePathEnvs(scope, preexisting, branches...)
			}
		case *ast.GoStmt:
			walkExpr(s.Call, scope)
		case *ast.DeferStmt:
			walkExpr(s.Call, scope)
		case *ast.SendStmt:
			walkExpr(s.Chan, scope)
			walkExpr(s.Value, scope)
		case *ast.LabeledStmt:
			walkStmt(s.Stmt, scope)
		}
	}

	walkBlock = func(block *ast.BlockStmt, scope *pathScope) {
		if block == nil {
			return
		}
		for _, stmt := range block.List {
			walkStmt(stmt, scope)
		}
	}

	walkBlock(block, scope)
	return found
}

// scanReloadOwnershipSource analyzes one Go source buffer for forbidden reload shapes.
func scanReloadOwnershipSource(filename, src string) (reloadOwnershipScanResult, error) {
	return scanReloadOwnershipOverlay(map[string]string{filename: src})
}

// scanReloadOwnershipOverlay analyzes one or more Go sources with package-aware
// and cross-package declaration indexing for active-runtime mutation and
// evidence-based config poll/watch detection.
func scanReloadOwnershipOverlay(files map[string]string) (reloadOwnershipScanResult, error) {
	var res reloadOwnershipScanResult
	if len(files) == 0 {
		return res, nil
	}

	filenames := make([]string, 0, len(files))
	for filename := range files {
		filenames = append(filenames, filename)
	}
	sort.Strings(filenames)

	parsed := make([]*parsedOverlayFile, 0, len(files))
	byDir := map[string][]*parsedOverlayFile{}
	for _, filename := range filenames {
		src := files[filename]
		fset, f, err := parseGoSource(filename, src)
		if err != nil {
			return res, err
		}
		pf := &parsedOverlayFile{
			filename:    filename,
			fset:        fset,
			file:        f,
			pkgDir:      filepath.ToSlash(filepath.Dir(filename)),
			pkgPath:     packagePathFromFilename(filename),
			aliasToPath: importAliasToPath(f),
		}
		parsed = append(parsed, pf)
		byDir[pf.pkgDir] = append(byDir[pf.pkgDir], pf)
	}

	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		group := byDir[dir]
		sort.Slice(group, func(i, j int) bool {
			return group[i].filename < group[j].filename
		})
		byDir[dir] = group
	}

	exports := newExportTypeIndex()
	pkgIndexByDir := map[string]*pkgTypeIndex{}
	indexes := make([]*pkgTypeIndex, 0, len(dirs))

	// Stage 1: parse-complete base indexes for every package (types, direct
	// methods, interface declarations, embed edges) without requiring peer exports.
	for _, dir := range dirs {
		idx := buildPkgTypeIndex(byDir[dir], exports)
		pkgIndexByDir[dir] = idx
		indexes = append(indexes, idx)
	}

	// Stage 2: publish the complete export/type map from base indexes.
	for _, dir := range dirs {
		idx := pkgIndexByDir[dir]
		idx.exports = exports
		if idx.pkgPath != "" {
			populateExportIndex(exports, idx.pkgPath, idx)
		}
	}

	// Stage 3: propagate local and cross-package embedded interface method sets
	// to a fixpoint, refreshing exports after each package that grows.
	bound := embedFixpointBound(indexes)
	converged := false
	for range bound {
		changed := false
		for _, dir := range dirs {
			idx := pkgIndexByDir[dir]
			grew, err := propagatePkgEmbeds(idx)
			if err != nil {
				return res, err
			}
			if grew {
				changed = true
				if idx.pkgPath != "" {
					populateExportIndex(exports, idx.pkgPath, idx)
				}
			}
		}
		if !changed {
			converged = true
			break
		}
	}
	if !converged {
		return res, fmt.Errorf("embedded interface method-set fixpoint exceeded bound %d", bound)
	}

	pollCtxByDir := map[string]*pollPkgContext{}
	for _, dir := range dirs {
		ctx, err := buildPollPkgContext(byDir[dir])
		if err != nil {
			return res, err
		}
		pollCtxByDir[dir] = ctx
	}

	for _, pf := range parsed {
		idx := pkgIndexByDir[pf.pkgDir]
		res.TracerBootstraps = append(res.TracerBootstraps, findTracerBootstraps(pf.fset, pf.file)...)
		res.MetricsConstructions = append(res.MetricsConstructions, findMetricsConstructions(pf.fset, pf.file)...)
		res.ProcessWorkers = append(res.ProcessWorkers, findProcessWorkerConstructions(pf.fset, pf.file)...)
		res.MutationSetters = append(res.MutationSetters, findActiveRuntimeMutationSetters(pf.fset, pf.file, idx, pf.aliasToPath)...)
		res.WatcherMechanisms = append(res.WatcherMechanisms, findConfigWatcherMechanisms(pf.fset, pf.file, pf.filename, pollCtxByDir[pf.pkgDir])...)
	}
	return res, nil
}
