package archtest

import (
	"go/ast"
	"go/token"
	"maps"
	"strconv"
	"strings"
)

// Lexical function-body analysis for protected function-value aliases.
//
// State model:
//   - Each aliasScope level holds bindings introduced in that lexical block.
//   - lookup walks parents; assign updates the declaring level (or defines locally
//     if unbound). Plain nested blocks share the parent chain so sequential
//     reassignment kills outer provenance after the block.
//   - Branching constructs (if/else, switch, type switch, select) and loops fork
//     a deep-cloned snapshot per path, then merge conservatively into the outer
//     state: a name stays protected if any reachable path still has protected
//     provenance; provenance is killed only when every reachable path definitely
//     binds a non-protected value. Missing default / false if-branch / zero
//     loop iterations are included as identity paths where the construct may
//     skip its body.
//   - Function literals analyze against an isolated clone (captures readable;
//     body assignments never mutate the enclosing state). Protected calls inside
//     the literal are still reported.
//   - Assignments evaluate every RHS against one pre-write snapshot, then apply
//     all LHS writes (Go simultaneous-assignment semantics). := shadows only
//     names not already declared in the current block.
//
// Supported (parser-level, deterministic):
//   - f := pkg.Protected / var f = pkg.Protected / parenthesized values
//   - alias chains f := pkg.Protected; g := f; g(...)
//   - sequential reassignment kills provenance in the binding's scope
//   - nested lexical shadowing does not retain outer provenance inside the
//     shadow and does not destroy the outer binding after scope exit
//   - direct selectors via canonical import paths / renamed imports
//   - dot-imported protected packages (unqualified names resolve)
//   - practical if/else, switch/select, loop, and uninvoked-closure shapes above
//
// Supported (Task 5.1 package-scope extension):
//   - package-level var / const function-value aliases of protected symbols when
//     the file-scope binding is seeded into each function walk
//
// Unsupported (disclosed; not claimed):
//   - aliases stored in structs/slices/maps or returned across functions
//   - reflection, unsafe, or go:linkname indirection
//   - method values (recv.M) and interface-held function values
//   - full CFG fixpoints / multi-iteration loop widening beyond zero∪one
//   - interprocedural or cross-package alias tracking

type aliasScope struct {
	parent  *aliasScope
	aliases map[string]string // name -> protected short label, or "" if known non-protected
}

func newAliasScope(parent *aliasScope) *aliasScope {
	return &aliasScope{parent: parent, aliases: map[string]string{}}
}

func (s *aliasScope) lookup(name string) (label string, ok bool) {
	for cur := s; cur != nil; cur = cur.parent {
		if v, exists := cur.aliases[name]; exists {
			return v, true
		}
	}
	return "", false
}

func (s *aliasScope) define(name, label string) {
	s.aliases[name] = label
}

func (s *aliasScope) assign(name, label string) {
	for cur := s; cur != nil; cur = cur.parent {
		if _, exists := cur.aliases[name]; exists {
			cur.aliases[name] = label
			return
		}
	}
	s.aliases[name] = label
}

// clone deep-copies the scope chain so forks can mutate independently.
func (s *aliasScope) clone() *aliasScope {
	if s == nil {
		return nil
	}
	return &aliasScope{
		parent:  s.parent.clone(),
		aliases: maps.Clone(s.aliases),
	}
}

// copyAliasesFrom replaces aliases along parallel chains (same depth).
func (s *aliasScope) copyAliasesFrom(src *aliasScope) {
	for d, c := s, src; d != nil && c != nil; d, c = d.parent, c.parent {
		d.aliases = maps.Clone(c.aliases)
	}
}

// mergeAliasScopes conservatively joins two forked chains of equal structure.
// A binding stays protected if either side still carries a non-empty label.
func mergeAliasScopes(a, b *aliasScope) *aliasScope {
	if a == nil {
		return b.clone()
	}
	if b == nil {
		return a.clone()
	}
	out := &aliasScope{
		parent:  mergeAliasScopes(a.parent, b.parent),
		aliases: map[string]string{},
	}
	for name, av := range a.aliases {
		bv, bOk := b.aliases[name]
		out.aliases[name] = mergeProvenance(true, av, bOk, bv)
	}
	for name, bv := range b.aliases {
		if _, aOk := a.aliases[name]; aOk {
			continue
		}
		out.aliases[name] = mergeProvenance(false, "", true, bv)
	}
	return out
}

func mergeProvenance(aOk bool, av string, bOk bool, bv string) string {
	// Protected if any reachable side still has protected provenance.
	if aOk && av != "" {
		return av
	}
	if bOk && bv != "" {
		return bv
	}
	// Both absent or both non-protected → known non-protected when either side
	// recorded a binding; if only one side introduced a block-local name, keep
	// it as non-protected so it does not escape as protected noise.
	if aOk || bOk {
		return ""
	}
	return ""
}

func mergeAliasScopeList(scopes []*aliasScope) *aliasScope {
	if len(scopes) == 0 {
		return nil
	}
	out := scopes[0]
	for _, s := range scopes[1:] {
		out = mergeAliasScopes(out, s)
	}
	return out
}

// absorbOuterWrites copies outer (parent-chain) bindings from a forked analysis
// root back into the live scope. forkRoot is newAliasScope(scope.clone()), so
// forkRoot.parent parallels scope.
func absorbOuterWrites(scope, forkRoot *aliasScope) {
	if scope == nil || forkRoot == nil || forkRoot.parent == nil {
		return
	}
	scope.copyAliasesFrom(forkRoot.parent)
}

// protectedSymbolSet maps short labels (e.g. "runtimebundle.Build") that a gate tracks.
type protectedSymbolSet map[string]bool

// callSiteOrdinals tracks per-enclosing-function occurrence counts keyed by short label.
type callSiteOrdinals map[string]int

func nextCallIdentity(encl, shortLabel string, ordinals callSiteOrdinals) string {
	key := orUnknown(encl) + "->" + shortLabel
	ordinals[key]++
	return "call:" + key + "#" + strconv.Itoa(ordinals[key])
}

func unwrapParen(expr ast.Expr) ast.Expr {
	for {
		p, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = p.X
	}
}

func dotImportedProtectedPaths(f *ast.File, protectedPaths map[string]bool) []string {
	var out []string
	for _, imp := range f.Imports {
		if imp.Name == nil || imp.Name.Name != "." {
			continue
		}
		path := strings.Trim(imp.Path.Value, `"`)
		if protectedPaths[path] {
			out = append(out, path)
		}
	}
	return out
}

// resolveProtectedFuncValue resolves expr to a protected short label when it is a
// direct selector, same-package name, dot-import name, or tracked local alias.
func resolveProtectedFuncValue(
	expr ast.Expr,
	scope *aliasScope,
	importAliases map[string]string,
	dotPaths []string,
	localUnqualified map[string]string, // unqualified name -> short label when same-package
	protected protectedSymbolSet,
) (string, bool) {
	expr = unwrapParen(expr)
	switch e := expr.(type) {
	case *ast.Ident:
		if label, ok := scope.lookup(e.Name); ok {
			if label == "" || !protected[label] {
				return "", false
			}
			return label, true
		}
		if label, ok := localUnqualified[e.Name]; ok && protected[label] {
			return label, true
		}
		for _, path := range dotPaths {
			if label, ok := protectedLabelFor(path, e.Name); ok && protected[label] {
				return label, true
			}
		}
		return "", false
	case *ast.SelectorExpr:
		pkg, ok := e.X.(*ast.Ident)
		if !ok || e.Sel == nil {
			return "", false
		}
		path := importAliases[pkg.Name]
		if path == "" {
			return "", false
		}
		label, ok := protectedLabelFor(path, e.Sel.Name)
		if !ok || !protected[label] {
			return "", false
		}
		return label, true
	default:
		return "", false
	}
}

func protectedLabelFor(importPath, name string) (string, bool) {
	switch importPath {
	case importRuntimebundle:
		switch name {
		case "Build", "BuildBootstrap", "BuildHost", "AttachReloadHost",
			"NewProcessServices", "CompileGeneration",
			"LoadBootstrapEffective", "LoadBootstrapEffectiveWithSource":
			return "runtimebundle." + name, true
		}
	case importStdhttp:
		switch name {
		case "RunWithRuntime", "RunWithGenerationHost":
			return "stdhttp." + name, true
		}
	case importRuntimehost:
		if name == "NewManager" {
			return "runtimehost.NewManager", true
		}
	case importCoreConfig:
		if name == "LoadEffective" {
			return "config.LoadEffective", true
		}
	case importLipruntime:
		if name == "Build" {
			return "lipruntime.Build", true
		}
	}
	return "", false
}

// shortLabelForGate maps a resolved protected label to the gate's allowlist spelling.
// Some gates use shorter forms (requestPlaneAsBuilt, LoadBootstrapEffective).
type shortLabelForGate func(resolved string) (string, bool)

type protectedCallVisitor struct {
	importAliases    map[string]string
	dotPaths         []string
	localUnqualified map[string]string
	protected        protectedSymbolSet
	toShort          shortLabelForGate
	encl             string
	ordinals         callSiteOrdinals
	onCall           func(identity string, call *ast.CallExpr, shortLabel string)
}

func (v *protectedCallVisitor) resolve(expr ast.Expr, scope *aliasScope) (string, bool) {
	resolved, ok := resolveProtectedFuncValue(expr, scope, v.importAliases, v.dotPaths, v.localUnqualified, v.protected)
	if !ok {
		return "", false
	}
	return v.toShort(resolved)
}

func (v *protectedCallVisitor) walkFunc(fd *ast.FuncDecl) {
	v.walkFuncWithPackageScope(fd, nil)
}

func (v *protectedCallVisitor) walkFuncWithPackageScope(fd *ast.FuncDecl, pkgScope *aliasScope) {
	if fd == nil || fd.Body == nil {
		return
	}
	scope := newAliasScope(pkgScope)
	if fd.Type != nil && fd.Type.Params != nil {
		for _, field := range fd.Type.Params.List {
			for _, name := range field.Names {
				if name != nil && name.Name != "_" {
					scope.define(name.Name, "")
				}
			}
		}
	}
	v.encl = fd.Name.Name
	v.walkBlock(fd.Body, scope)
}

// packageScopeProtectedAliases seeds file-scope var/const bindings whose RHS
// resolves to a protected function value (Task 5.1 package-scope evasion).
func packageScopeProtectedAliases(
	f *ast.File,
	importAliases map[string]string,
	dotPaths []string,
	localUnqualified map[string]string,
	protected protectedSymbolSet,
) *aliasScope {
	scope := newAliasScope(nil)
	if f == nil {
		return scope
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || (gd.Tok != token.VAR && gd.Tok != token.CONST) {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if name == nil || name.Name == "_" || i >= len(vs.Values) {
					continue
				}
				resolved, ok := resolveProtectedFuncValue(vs.Values[i], scope, importAliases, dotPaths, localUnqualified, protected)
				if !ok {
					scope.define(name.Name, "")
					continue
				}
				scope.define(name.Name, resolved)
			}
		}
	}
	return scope
}

func (v *protectedCallVisitor) walkBlock(block *ast.BlockStmt, scope *aliasScope) {
	if block == nil {
		return
	}
	for _, stmt := range block.List {
		v.walkStmt(stmt, scope)
	}
}

func (v *protectedCallVisitor) walkStmt(stmt ast.Stmt, scope *aliasScope) {
	if stmt == nil {
		return
	}
	switch s := stmt.(type) {
	case *ast.AssignStmt:
		for _, rhs := range s.Rhs {
			v.walkExpr(rhs, scope)
		}
		v.bindAssign(s, scope)
	case *ast.DeclStmt:
		gd, ok := s.Decl.(*ast.GenDecl)
		if !ok || (gd.Tok != token.VAR && gd.Tok != token.CONST) {
			return
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, val := range vs.Values {
				v.walkExpr(val, scope)
			}
			v.bindValueSpec(vs, scope)
		}
	case *ast.ExprStmt:
		v.walkExpr(s.X, scope)
	case *ast.ReturnStmt:
		for _, r := range s.Results {
			v.walkExpr(r, scope)
		}
	case *ast.DeferStmt:
		v.walkExpr(s.Call, scope)
	case *ast.GoStmt:
		v.walkExpr(s.Call, scope)
	case *ast.SendStmt:
		v.walkExpr(s.Chan, scope)
		v.walkExpr(s.Value, scope)
	case *ast.IncDecStmt:
		v.walkExpr(s.X, scope)
	case *ast.BlockStmt:
		// Sequential nested block: child may shadow; assign still updates outer.
		v.walkBlock(s, newAliasScope(scope))
	case *ast.IfStmt:
		v.walkIf(s, scope)
	case *ast.ForStmt:
		v.walkFor(s, scope)
	case *ast.RangeStmt:
		v.walkRange(s, scope)
	case *ast.SwitchStmt:
		v.walkSwitch(s, scope)
	case *ast.TypeSwitchStmt:
		v.walkTypeSwitch(s, scope)
	case *ast.SelectStmt:
		v.walkSelect(s, scope)
	case *ast.LabeledStmt:
		v.walkStmt(s.Stmt, scope)
	case *ast.CaseClause:
		for _, e := range s.List {
			v.walkExpr(e, scope)
		}
		for _, cs := range s.Body {
			v.walkStmt(cs, scope)
		}
	}
}

func (v *protectedCallVisitor) walkIf(s *ast.IfStmt, scope *aliasScope) {
	// Init bindings are scoped to the if; outer writes go to a cloned parent chain.
	base := newAliasScope(scope.clone())
	if s.Init != nil {
		v.walkStmt(s.Init, base)
	}
	v.walkExpr(s.Cond, base)

	thenScope := base.clone()
	v.walkBlock(s.Body, thenScope)

	elseScope := base.clone()
	if s.Else != nil {
		v.walkStmt(s.Else, elseScope)
	}
	// No-else path is identity on base (cond may be false).

	merged := mergeAliasScopes(thenScope, elseScope)
	absorbOuterWrites(scope, merged)
}

func (v *protectedCallVisitor) walkFor(s *ast.ForStmt, scope *aliasScope) {
	base := newAliasScope(scope.clone())
	if s.Init != nil {
		v.walkStmt(s.Init, base)
	}
	v.walkExpr(s.Cond, base)

	zero := base.clone() // zero-iteration / never-taken body
	bodyScope := base.clone()
	v.walkBlock(s.Body, bodyScope)
	if s.Post != nil {
		v.walkStmt(s.Post, bodyScope)
	}

	merged := mergeAliasScopes(zero, bodyScope)
	absorbOuterWrites(scope, merged)
}

func (v *protectedCallVisitor) walkRange(s *ast.RangeStmt, scope *aliasScope) {
	v.walkExpr(s.X, scope)

	zero := newAliasScope(scope.clone()) // zero iterations: outer unchanged
	bodyScope := newAliasScope(scope.clone())
	if s.Tok == token.DEFINE {
		if id, ok := s.Key.(*ast.Ident); ok && id.Name != "_" {
			bodyScope.define(id.Name, "")
		}
		if id, ok := s.Value.(*ast.Ident); ok && id.Name != "_" {
			bodyScope.define(id.Name, "")
		}
	} else {
		if id, ok := s.Key.(*ast.Ident); ok && id.Name != "_" {
			bodyScope.assign(id.Name, "")
		}
		if id, ok := s.Value.(*ast.Ident); ok && id.Name != "_" {
			bodyScope.assign(id.Name, "")
		}
	}
	v.walkBlock(s.Body, bodyScope)

	merged := mergeAliasScopes(zero, bodyScope)
	absorbOuterWrites(scope, merged)
}

func (v *protectedCallVisitor) walkSwitch(s *ast.SwitchStmt, scope *aliasScope) {
	base := newAliasScope(scope.clone())
	if s.Init != nil {
		v.walkStmt(s.Init, base)
	}
	v.walkExpr(s.Tag, base)

	var branches []*aliasScope
	hasDefault := false
	if s.Body != nil {
		for _, stmt := range s.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			if cc.List == nil {
				hasDefault = true
			}
			caseScope := base.clone()
			for _, e := range cc.List {
				v.walkExpr(e, caseScope)
			}
			for _, cs := range cc.Body {
				v.walkStmt(cs, caseScope)
			}
			branches = append(branches, caseScope)
		}
	}
	if !hasDefault {
		branches = append(branches, base.clone()) // no case matches
	}
	if len(branches) == 0 {
		return
	}
	absorbOuterWrites(scope, mergeAliasScopeList(branches))
}

func (v *protectedCallVisitor) walkTypeSwitch(s *ast.TypeSwitchStmt, scope *aliasScope) {
	base := newAliasScope(scope.clone())
	if s.Init != nil {
		v.walkStmt(s.Init, base)
	}
	if s.Assign != nil {
		v.walkStmt(s.Assign, base)
	}

	var branches []*aliasScope
	hasDefault := false
	if s.Body != nil {
		for _, stmt := range s.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			if cc.List == nil {
				hasDefault = true
			}
			caseScope := base.clone()
			for _, cs := range cc.Body {
				v.walkStmt(cs, caseScope)
			}
			branches = append(branches, caseScope)
		}
	}
	if !hasDefault {
		branches = append(branches, base.clone())
	}
	if len(branches) == 0 {
		return
	}
	absorbOuterWrites(scope, mergeAliasScopeList(branches))
}

func (v *protectedCallVisitor) walkSelect(s *ast.SelectStmt, scope *aliasScope) {
	if s.Body == nil {
		return
	}
	var branches []*aliasScope
	for _, stmt := range s.Body.List {
		cc, ok := stmt.(*ast.CommClause)
		if !ok {
			continue
		}
		caseScope := newAliasScope(scope.clone())
		if cc.Comm != nil {
			v.walkStmt(cc.Comm, caseScope)
		}
		for _, cs := range cc.Body {
			v.walkStmt(cs, caseScope)
		}
		branches = append(branches, caseScope)
	}
	// Select always takes exactly one case (default is a CommClause with nil Comm).
	if len(branches) == 0 {
		return
	}
	absorbOuterWrites(scope, mergeAliasScopeList(branches))
}

func (v *protectedCallVisitor) bindAssign(s *ast.AssignStmt, scope *aliasScope) {
	define := s.Tok == token.DEFINE
	if len(s.Lhs) == len(s.Rhs) {
		// Evaluate every RHS from one pre-write snapshot, then apply writes.
		labels := make([]string, len(s.Rhs))
		for i, rhs := range s.Rhs {
			label, _ := resolveProtectedFuncValue(rhs, scope, v.importAliases, v.dotPaths, v.localUnqualified, v.protected)
			if !v.protected[label] {
				label = ""
			}
			labels[i] = label
		}
		for i, lhs := range s.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || id.Name == "_" {
				continue
			}
			label := labels[i]
			if define {
				// := declares only names not already present in this block;
				// existing same-block names are assigned in place (not parents).
				if _, exists := scope.aliases[id.Name]; exists {
					scope.aliases[id.Name] = label
				} else {
					scope.define(id.Name, label)
				}
			} else {
				scope.assign(id.Name, label)
			}
		}
		return
	}
	// Multi-value RHS: cannot track provenance; kill LHS bindings.
	for _, lhs := range s.Lhs {
		id, ok := lhs.(*ast.Ident)
		if !ok || id.Name == "_" {
			continue
		}
		if define {
			if _, exists := scope.aliases[id.Name]; exists {
				scope.aliases[id.Name] = ""
			} else {
				scope.define(id.Name, "")
			}
		} else {
			scope.assign(id.Name, "")
		}
	}
}

func (v *protectedCallVisitor) bindValueSpec(vs *ast.ValueSpec, scope *aliasScope) {
	if len(vs.Values) == 0 {
		for _, name := range vs.Names {
			if name != nil && name.Name != "_" {
				scope.define(name.Name, "")
			}
		}
		return
	}
	if len(vs.Names) == len(vs.Values) {
		labels := make([]string, len(vs.Values))
		for i, val := range vs.Values {
			label, _ := resolveProtectedFuncValue(val, scope, v.importAliases, v.dotPaths, v.localUnqualified, v.protected)
			if !v.protected[label] {
				label = ""
			}
			labels[i] = label
		}
		for i, name := range vs.Names {
			if name == nil || name.Name == "_" {
				continue
			}
			scope.define(name.Name, labels[i])
		}
		return
	}
	for _, name := range vs.Names {
		if name != nil && name.Name != "_" {
			scope.define(name.Name, "")
		}
	}
}

func (v *protectedCallVisitor) walkExpr(expr ast.Expr, scope *aliasScope) {
	if expr == nil {
		return
	}
	switch e := expr.(type) {
	case *ast.CallExpr:
		v.walkExpr(e.Fun, scope)
		for _, a := range e.Args {
			v.walkExpr(a, scope)
		}
		if short, ok := v.resolve(e.Fun, scope); ok {
			id := nextCallIdentity(v.encl, short, v.ordinals)
			v.onCall(id, e, short)
		}
	case *ast.ParenExpr:
		v.walkExpr(e.X, scope)
	case *ast.SelectorExpr:
		v.walkExpr(e.X, scope)
	case *ast.IndexExpr:
		v.walkExpr(e.X, scope)
		v.walkExpr(e.Index, scope)
	case *ast.IndexListExpr:
		v.walkExpr(e.X, scope)
		for _, idx := range e.Indices {
			v.walkExpr(idx, scope)
		}
	case *ast.SliceExpr:
		v.walkExpr(e.X, scope)
		v.walkExpr(e.Low, scope)
		v.walkExpr(e.High, scope)
		v.walkExpr(e.Max, scope)
	case *ast.StarExpr:
		v.walkExpr(e.X, scope)
	case *ast.UnaryExpr:
		v.walkExpr(e.X, scope)
	case *ast.BinaryExpr:
		v.walkExpr(e.X, scope)
		v.walkExpr(e.Y, scope)
	case *ast.KeyValueExpr:
		v.walkExpr(e.Key, scope)
		v.walkExpr(e.Value, scope)
	case *ast.CompositeLit:
		v.walkExpr(e.Type, scope)
		for _, elt := range e.Elts {
			v.walkExpr(elt, scope)
		}
	case *ast.TypeAssertExpr:
		v.walkExpr(e.X, scope)
	case *ast.FuncLit:
		// Isolated clone: captures are readable; body writes do not escape.
		iso := newAliasScope(scope.clone())
		if e.Type != nil && e.Type.Params != nil {
			for _, field := range e.Type.Params.List {
				for _, name := range field.Names {
					if name != nil && name.Name != "_" {
						iso.define(name.Name, "")
					}
				}
			}
		}
		prev := v.encl
		if prev == "" {
			v.encl = "funcLit"
		}
		v.walkBlock(e.Body, iso)
		v.encl = prev
	case *ast.Ident, *ast.BasicLit, *ast.BadExpr, *ast.Ellipsis:
		// leaf / no nested calls of interest
	case *ast.ArrayType, *ast.StructType, *ast.FuncType, *ast.InterfaceType, *ast.MapType, *ast.ChanType:
		// type expressions
	default:
		ast.Inspect(expr, func(n ast.Node) bool {
			if n == nil || n == expr {
				return true
			}
			switch x := n.(type) {
			case *ast.FuncLit:
				v.walkExpr(x, scope)
				return false
			case *ast.CallExpr:
				v.walkExpr(x, scope)
				return false
			}
			return true
		})
	}
}

func scanDotImportFindings(gate, rel string, fset *token.FileSet, f *ast.File, protectedPaths map[string]bool) []convergenceFinding {
	var out []convergenceFinding
	for _, imp := range f.Imports {
		if imp.Name == nil || imp.Name.Name != "." {
			continue
		}
		path := strings.Trim(imp.Path.Value, `"`)
		if !protectedPaths[path] {
			continue
		}
		short := path
		if i := strings.LastIndex(path, "/"); i >= 0 {
			short = path[i+1:]
		}
		out = append(out, convergenceFinding{
			Gate: gate, Path: rel,
			Identity:       "dotimport:" + short,
			Classification: classCall,
			Detail:         formatPos(fset, imp.Pos()) + " dot-import of protected package " + path,
		})
	}
	return out
}
