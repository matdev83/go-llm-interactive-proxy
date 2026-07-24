package runtimehost

// Task 6.4 permanent architecture enforcement: ReloadState is the sole
// production owner of active effective/source, last result/success/failure,
// source-integrity posture, safe model-generation fingerprint, and bounded
// canonical history. Enforcement reuses Task 6.2/6.3 provenance/AST helpers
// (parseProductionRuntimehostFiles, collectPackageTypeAliases, resolveTypeString,
// collectNamedConstructorCallables, mustParseSyntheticFiles, violationContains)
// rather than syntax-only exact-name blacklists.

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"
	"testing"
)

// TestReloadState_SingleOwnerDeclaration proves exactly one production
// ReloadState struct declaration, and it lives in reload_state.go.
func TestReloadState_SingleOwnerDeclaration(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files := parseProductionRuntimehostFiles(t, fset)
	if got := analyzeReloadStateOwnership(files); len(got) > 0 {
		t.Fatalf("ReloadState ownership violations:\n%s", strings.Join(got, "\n"))
	}
}

// TestReloadState_SoleConstructionSite proves exactly one direct newReloadState
// call in NewCoordinator, rejects constructor aliases/wrappers, and requires
// exactly one concrete ReloadState allocation inside newReloadState.
func TestReloadState_SoleConstructionSite(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files := parseProductionRuntimehostFiles(t, fset)
	if got := analyzeReloadStateConstructorGraph(files); len(got) > 0 {
		t.Fatalf("newReloadState constructor graph violations:\n%s", strings.Join(got, "\n"))
	}
}

// TestReloadState_SoleCompleteStateShape proves ReloadState is the only
// complete state-shape owner (mutex + active eff/src + ≥3 Result slots +
// posture strings + bounded HistoryEntry storage). Renamed equivalent graphs
// and split complete authority are rejected; partial structs remain accepted.
func TestReloadState_SoleCompleteStateShape(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files := parseProductionRuntimehostFiles(t, fset)
	if got := analyzeReloadStateCompleteShape(files); len(got) > 0 {
		t.Fatalf("ReloadState complete-shape ownership violations:\n%s", strings.Join(got, "\n"))
	}
}

// TestReloadState_ExactCallerSites proves ActiveInput/Apply are called only
// from Coordinator.Reload (exactly twice each) and Snapshot only from
// Coordinator.Status (exactly once), including alias/method-value rejection.
func TestReloadState_ExactCallerSites(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files := parseProductionRuntimehostFiles(t, fset)

	for _, tc := range []struct {
		method string
		want   int
		fn     string
	}{
		{method: "ActiveInput", want: 2, fn: "Coordinator.Reload"},
		{method: "Apply", want: 2, fn: "Coordinator.Reload"},
		{method: "Snapshot", want: 1, fn: "Coordinator.Status"},
	} {
		sites, methodVals := findReloadStateMethodSites(files, tc.method)
		if len(methodVals) > 0 {
			t.Fatalf("method-value aliases of state.%s forbidden:\n%s", tc.method, strings.Join(methodVals, "\n"))
		}
		for _, s := range sites {
			if s.file != "coordinator.go" || !strings.HasSuffix(s.fn, tc.fn) {
				t.Fatalf("unexpected state.%s caller outside %s: %s:%s", tc.method, tc.fn, s.file, s.fn)
			}
		}
		if len(sites) != tc.want {
			t.Fatalf("want exactly %d state.%s call site(s) in %s; got %d", tc.want, tc.method, tc.fn, len(sites))
		}
	}
}

// TestReloadState_ObserverHasNoHistoryOwnership proves ReloadObserver no
// longer owns StatusHistory or canonical HistoryEntry collections.
func TestReloadState_ObserverHasNoHistoryOwnership(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files := parseProductionRuntimehostFiles(t, fset)
	if got := scanObserverForHistoryOwnership(files); len(got) > 0 {
		t.Fatalf("ReloadObserver history ownership violations:\n%s", strings.Join(got, "\n"))
	}
}

// TestReloadState_NoForbiddenDependencies proves ReloadState stores/depends on
// no Manager/gate/runner/observer/source/loader/compiler under aliases, and
// carries no generic callback/hook fields.
func TestReloadState_NoForbiddenDependencies(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files := parseProductionRuntimehostFiles(t, fset)
	if got := scanReloadStateForbiddenDependencies(files); len(got) > 0 {
		t.Fatalf("ReloadState forbidden dependency violations:\n%s", strings.Join(got, "\n"))
	}
}

// --- analyzers ---

func analyzeReloadStateOwnership(files map[string]*ast.File) []string {
	var violations []string
	aliases := collectPackageTypeAliases(files)

	var stateFiles []string
	for path, file := range files {
		ts := findTypeSpec(file, "ReloadState")
		if ts == nil {
			continue
		}
		if _, ok := ts.Type.(*ast.StructType); ok && ts.Assign == 0 {
			stateFiles = append(stateFiles, path)
		}
	}
	if len(stateFiles) != 1 || stateFiles[0] != "reload_state.go" {
		violations = append(violations, fmt.Sprintf("want exactly one ReloadState struct declaration in reload_state.go; got %v", stateFiles))
	}

	for path, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil || ts.Name.Name == "ReloadState" {
					continue
				}
				under := resolveTypeString(ts.Type, aliases)
				if under == "ReloadState" || under == "*ReloadState" {
					kind := "defined type"
					if ts.Assign != 0 {
						kind = "alias"
					}
					violations = append(violations, fmt.Sprintf("%s: %s %q of ReloadState/*ReloadState", path, kind, ts.Name.Name))
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				if ts.Name.Name == "Coordinator" {
					stateFields := 0
					for _, f := range st.Fields.List {
						if !storesReloadState(f.Type, aliases) {
							continue
						}
						for _, n := range f.Names {
							if n.Name != "state" {
								violations = append(violations, fmt.Sprintf("%s: Coordinator ReloadState field must be named state; got %q", path, n.Name))
							} else {
								stateFields++
							}
						}
						if len(f.Names) == 0 {
							violations = append(violations, fmt.Sprintf("%s: Coordinator must not embed ReloadState", path))
						}
					}
					if stateFields != 1 {
						violations = append(violations, fmt.Sprintf("%s: Coordinator must have exactly one state *ReloadState field; got %d", path, stateFields))
					}
					continue
				}
				for _, f := range st.Fields.List {
					if storesReloadState(f.Type, aliases) {
						name := ts.Name.Name
						if len(f.Names) > 0 {
							name = ts.Name.Name + "." + f.Names[0].Name
						}
						violations = append(violations, fmt.Sprintf("%s: non-Coordinator struct %q stores/embeds ReloadState", path, name))
					}
				}
			}
		}
	}
	return violations
}

func storesReloadState(expr ast.Expr, aliases map[string]string) bool {
	under := resolveTypeString(expr, aliases)
	return under == "ReloadState" || under == "*ReloadState"
}

// analyzeReloadStateCompleteShape rejects any non-ReloadState type (including
// Coordinator) that carries the practical complete terminal/active/history
// state graph by resolved transitive owned type shape, not field names.
func analyzeReloadStateCompleteShape(files map[string]*ast.File) []string {
	var violations []string
	graph := buildPackageTypeGraph(files)
	for path, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil || ts.Name.Name == "ReloadState" {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				shape := aggregateOwnedReloadStateShape(ts.Name.Name, st, graph, nil)
				if shape.isComplete() {
					violations = append(violations, fmt.Sprintf("%s: type %q reintroduces complete ReloadState authority by shape", path, ts.Name.Name))
				}
			}
		}
	}
	return violations
}

// reloadStateShapeRoles aggregates lock/active/result/posture/history roles
// owned transitively through nested package structs.
type reloadStateShapeRoles struct {
	hasMutex    bool
	hasEff      bool
	hasSrc      bool
	resultSlots int
	stringSlots int
	hasHistory  bool
	hasHistCap  bool
}

func (r reloadStateShapeRoles) isComplete() bool {
	return r.hasMutex && r.hasEff && r.hasSrc && r.resultSlots >= 3 && r.stringSlots >= 2 && r.hasHistory && r.hasHistCap
}

func (r *reloadStateShapeRoles) merge(other reloadStateShapeRoles) {
	r.hasMutex = r.hasMutex || other.hasMutex
	r.hasEff = r.hasEff || other.hasEff
	r.hasSrc = r.hasSrc || other.hasSrc
	r.resultSlots += other.resultSlots
	r.stringSlots += other.stringSlots
	r.hasHistory = r.hasHistory || other.hasHistory
	r.hasHistCap = r.hasHistCap || other.hasHistCap
}

// packageTypeGraph is the shared resolved package struct/func type graph used
// by complete-shape, forbidden-collaborator, and observer-history scanners.
type packageTypeGraph struct {
	aliases   map[string]string
	structs   map[string]*ast.StructType
	funcTypes map[string]bool
	typeFiles map[string]string // type name -> declaring file basename
}

func buildPackageTypeGraph(files map[string]*ast.File) *packageTypeGraph {
	g := &packageTypeGraph{
		aliases:   collectPackageTypeAliases(files),
		structs:   map[string]*ast.StructType{},
		funcTypes: map[string]bool{},
		typeFiles: map[string]string{},
	}
	for path, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil {
					continue
				}
				g.typeFiles[ts.Name.Name] = path
				switch typ := ts.Type.(type) {
				case *ast.StructType:
					g.structs[ts.Name.Name] = typ
				case *ast.FuncType:
					g.funcTypes[ts.Name.Name] = true
				}
			}
		}
	}
	// Close function-type names under alias/defined-type chains.
	for range 8 {
		changed := false
		for path, file := range files {
			_ = path
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name == nil {
						continue
					}
					if g.funcTypes[ts.Name.Name] {
						continue
					}
					if _, ok := ts.Type.(*ast.FuncType); ok {
						g.funcTypes[ts.Name.Name] = true
						changed = true
						continue
					}
					under := resolveTypeString(ts.Type, g.aliases)
					under = strings.TrimPrefix(under, "*")
					if under != "" && g.funcTypes[under] {
						g.funcTypes[ts.Name.Name] = true
						changed = true
					}
				}
			}
		}
		if !changed {
			break
		}
	}
	// Alias map may already flatten to another name that is a func type.
	for name, under := range g.aliases {
		u := strings.TrimPrefix(under, "*")
		if g.funcTypes[u] {
			g.funcTypes[name] = true
		}
	}
	return g
}

func aggregateOwnedReloadStateShape(typeName string, st *ast.StructType, graph *packageTypeGraph, visiting map[string]bool) reloadStateShapeRoles {
	var out reloadStateShapeRoles
	if st == nil || st.Fields == nil {
		return out
	}
	if visiting == nil {
		visiting = map[string]bool{}
	}
	if typeName != "" {
		if visiting[typeName] {
			return out
		}
		visiting[typeName] = true
		defer delete(visiting, typeName)
	}
	for _, f := range st.Fields.List {
		n := fieldArity(f)
		child := aggregateShapeFromTypeExpr(f.Type, n, graph, visiting)
		out.merge(child)
	}
	return out
}

func aggregateShapeFromTypeExpr(expr ast.Expr, arity int, graph *packageTypeGraph, visiting map[string]bool) reloadStateShapeRoles {
	var out reloadStateShapeRoles
	if expr == nil {
		return out
	}
	expr = unwrapParen(expr)
	switch e := expr.(type) {
	case *ast.StarExpr:
		return aggregateShapeFromTypeExpr(e.X, arity, graph, visiting)
	case *ast.StructType:
		return aggregateOwnedReloadStateShape("", e, graph, visiting)
	}

	typ := resolveTypeString(expr, graph.aliases)
	if typ == "" {
		return out
	}
	// Canonical ReloadState is an opaque approved owner; do not attribute its
	// roles to containers that merely store *ReloadState (Coordinator).
	base := strings.TrimPrefix(typ, "*")
	if base == "ReloadState" {
		return out
	}
	switch {
	case isMutexType(typ):
		out.hasMutex = true
	case isActiveEffectiveType(typ):
		out.hasEff = true
	case isActiveSourceType(typ):
		out.hasSrc = true
	case isCanonicalResultType(typ):
		out.resultSlots += arity
	case isCanonicalHistoryCollection(typ):
		out.hasHistory = true
	case isHistoryCapacityType(typ) && !isMutexType(typ):
		// int-like capacity metadata; distinguished from string posture slots.
		if isIntLikeType(typ) {
			out.hasHistCap = true
		}
	case typ == "string":
		out.stringSlots += arity
	default:
		if st, ok := graph.structs[base]; ok {
			out.merge(aggregateOwnedReloadStateShape(base, st, graph, visiting))
		}
	}
	return out
}

func isActiveEffectiveType(typ string) bool {
	switch typ {
	case "*config.EffectiveConfig", "config.EffectiveConfig":
		return true
	default:
		return false
	}
}

func isActiveSourceType(typ string) bool {
	switch typ {
	case "*configsource.ActiveSourceVersion", "configsource.ActiveSourceVersion":
		return true
	default:
		return false
	}
}

func isCanonicalResultType(typ string) bool {
	switch typ {
	case "sdkreload.Result", "configreload.Result", "Result":
		return true
	default:
		return false
	}
}

func isCanonicalHistoryEntryType(typ string) bool {
	switch typ {
	case "sdkreload.HistoryEntry", "configreload.HistoryEntry", "HistoryEntry":
		return true
	default:
		return false
	}
}

func isCanonicalHistoryCollection(typ string) bool {
	if isStatusHistoryType(typ) {
		return true
	}
	if strings.HasPrefix(typ, "[]") && isCanonicalHistoryEntryType(strings.TrimPrefix(typ, "[]")) {
		return true
	}
	if strings.HasPrefix(typ, "map[") && strings.Contains(typ, "]") {
		elt := typ[strings.Index(typ, "]")+1:]
		return isCanonicalHistoryEntryType(elt)
	}
	return false
}

func isStatusHistoryType(typ string) bool {
	switch typ {
	case "configreload.StatusHistory", "*configreload.StatusHistory", "StatusHistory", "*StatusHistory":
		return true
	default:
		return false
	}
}

func isHistoryCapacityType(typ string) bool {
	return isIntLikeType(typ)
}

func isFuncTypedExpr(expr ast.Expr, graph *packageTypeGraph) bool {
	if expr == nil {
		return false
	}
	switch e := unwrapParen(expr).(type) {
	case *ast.FuncType:
		return true
	case *ast.StarExpr:
		return isFuncTypedExpr(e.X, graph)
	case *ast.Ident:
		if graph.funcTypes[e.Name] {
			return true
		}
		under := resolveAliasString(e.Name, graph.aliases)
		under = strings.TrimPrefix(under, "*")
		return graph.funcTypes[under]
	}
	typ := resolveTypeString(expr, graph.aliases)
	typ = strings.TrimPrefix(typ, "*")
	return typ != "" && graph.funcTypes[typ]
}

func namedTypeBase(typ string) string {
	typ = strings.TrimPrefix(typ, "*")
	if i := strings.Index(typ, "["); i >= 0 {
		return typ[:i]
	}
	return typ
}

func typeExprContainsForbiddenCollaborator(expr ast.Expr, graph *packageTypeGraph, visiting map[string]bool) string {
	if expr == nil {
		return ""
	}
	if visiting == nil {
		visiting = map[string]bool{}
	}
	expr = unwrapParen(expr)
	switch e := expr.(type) {
	case *ast.StarExpr:
		return typeExprContainsForbiddenCollaborator(e.X, graph, visiting)
	case *ast.ArrayType:
		return typeExprContainsForbiddenCollaborator(e.Elt, graph, visiting)
	case *ast.MapType:
		if got := typeExprContainsForbiddenCollaborator(e.Key, graph, visiting); got != "" {
			return got
		}
		return typeExprContainsForbiddenCollaborator(e.Value, graph, visiting)
	case *ast.StructType:
		if e.Fields == nil {
			return ""
		}
		for _, f := range e.Fields.List {
			if got := typeExprContainsForbiddenCollaborator(f.Type, graph, visiting); got != "" {
				return got
			}
			if isFuncTypedExpr(f.Type, graph) {
				return "func"
			}
		}
		return ""
	}

	typ := resolveTypeString(expr, graph.aliases)
	if forbiddenReloadStateCollaborators[typ] {
		return typ
	}
	base := namedTypeBase(typ)
	if forbiddenReloadStateCollaborators[base] || forbiddenReloadStateCollaborators["*"+base] {
		if forbiddenReloadStateCollaborators[typ] {
			return typ
		}
		if forbiddenReloadStateCollaborators["*"+base] {
			return "*" + base
		}
		return base
	}
	if base == "" || visiting[base] {
		return ""
	}
	st, ok := graph.structs[base]
	if !ok || st.Fields == nil {
		return ""
	}
	visiting[base] = true
	defer delete(visiting, base)
	for _, f := range st.Fields.List {
		if got := typeExprContainsForbiddenCollaborator(f.Type, graph, visiting); got != "" {
			return got
		}
		if isFuncTypedExpr(f.Type, graph) {
			return "func"
		}
	}
	return ""
}

func typeExprContainsFuncCallback(expr ast.Expr, graph *packageTypeGraph, visiting map[string]bool) bool {
	if expr == nil {
		return false
	}
	if visiting == nil {
		visiting = map[string]bool{}
	}
	if isFuncTypedExpr(expr, graph) {
		return true
	}
	expr = unwrapParen(expr)
	switch e := expr.(type) {
	case *ast.StarExpr:
		return typeExprContainsFuncCallback(e.X, graph, visiting)
	case *ast.ArrayType:
		return typeExprContainsFuncCallback(e.Elt, graph, visiting)
	case *ast.MapType:
		return typeExprContainsFuncCallback(e.Key, graph, visiting) || typeExprContainsFuncCallback(e.Value, graph, visiting)
	case *ast.StructType:
		if e.Fields == nil {
			return false
		}
		for _, f := range e.Fields.List {
			if typeExprContainsFuncCallback(f.Type, graph, visiting) {
				return true
			}
		}
		return false
	}
	typ := resolveTypeString(expr, graph.aliases)
	base := namedTypeBase(typ)
	if base == "" || visiting[base] {
		return false
	}
	st, ok := graph.structs[base]
	if !ok {
		return false
	}
	visiting[base] = true
	defer delete(visiting, base)
	return typeExprContainsFuncCallback(st, graph, visiting)
}

func isHookRegistryName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "hook") && strings.Contains(lower, "registry")
}

// analyzeReloadStateConstructorGraph requires exactly one direct newReloadState
// call in NewCoordinator, rejects package/local/chained aliases and wrappers,
// and requires exactly one concrete ReloadState allocation inside newReloadState.
func analyzeReloadStateConstructorGraph(files map[string]*ast.File) []string {
	var violations []string
	ctorCallables := collectNamedConstructorCallables(files, "newReloadState")

	for path, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if name == nil || name.Name == "newReloadState" || i >= len(vs.Values) {
						continue
					}
					if refersToConstructorCallable(vs.Values[i], ctorCallables) {
						violations = append(violations, fmt.Sprintf("%s: package-scope constructor alias %q of newReloadState", path, name.Name))
					}
				}
			}
		}
	}

	var calls []ctorCallRecord
	for path, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fnName := ""
			if fd.Name != nil {
				fnName = fd.Name.Name
			}
			localCallables := cloneStringSet(ctorCallables)
			var walk func(body *ast.BlockStmt)
			walk = func(body *ast.BlockStmt) {
				if body == nil {
					return
				}
				for _, stmt := range body.List {
					switch s := stmt.(type) {
					case *ast.AssignStmt:
						for i, rhs := range s.Rhs {
							if refersToConstructorCallable(rhs, localCallables) {
								if i < len(s.Lhs) {
									if id, ok := s.Lhs[i].(*ast.Ident); ok && id.Name != "_" && id.Name != "newReloadState" {
										localCallables[id.Name] = true
										violations = append(violations, fmt.Sprintf("%s: local constructor alias %q of newReloadState in %s", path, id.Name, fnName))
									}
								}
							}
							inspectNamedConstructorCalls(rhs, path, fnName, "newReloadState", localCallables, &calls)
						}
					case *ast.DeclStmt:
						gd, ok := s.Decl.(*ast.GenDecl)
						if !ok || gd.Tok != token.VAR {
							continue
						}
						for _, spec := range gd.Specs {
							vs, ok := spec.(*ast.ValueSpec)
							if !ok {
								continue
							}
							for i, name := range vs.Names {
								if name == nil {
									continue
								}
								if i < len(vs.Values) && refersToConstructorCallable(vs.Values[i], localCallables) {
									if name.Name != "newReloadState" {
										localCallables[name.Name] = true
										violations = append(violations, fmt.Sprintf("%s: local constructor alias %q of newReloadState in %s", path, name.Name, fnName))
									}
								}
								if i < len(vs.Values) {
									inspectNamedConstructorCalls(vs.Values[i], path, fnName, "newReloadState", localCallables, &calls)
								}
							}
						}
					case *ast.DeferStmt:
						if s.Call == nil {
							continue
						}
						if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
							walk(lit.Body)
							continue
						}
						inspectNamedConstructorCalls(s.Call, path, fnName, "newReloadState", localCallables, &calls)
					case *ast.GoStmt:
						if s.Call == nil {
							continue
						}
						if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
							walk(lit.Body)
							continue
						}
						inspectNamedConstructorCalls(s.Call, path, fnName, "newReloadState", localCallables, &calls)
					case *ast.IfStmt:
						if s.Init != nil {
							if as, ok := s.Init.(*ast.AssignStmt); ok {
								for i, rhs := range as.Rhs {
									if refersToConstructorCallable(rhs, localCallables) {
										if i < len(as.Lhs) {
											if id, ok := as.Lhs[i].(*ast.Ident); ok && id.Name != "_" && id.Name != "newReloadState" {
												localCallables[id.Name] = true
												violations = append(violations, fmt.Sprintf("%s: local constructor alias %q of newReloadState in %s", path, id.Name, fnName))
											}
										}
									}
									inspectNamedConstructorCalls(rhs, path, fnName, "newReloadState", localCallables, &calls)
								}
							}
						}
						walk(s.Body)
						if s.Else != nil {
							if b, ok := s.Else.(*ast.BlockStmt); ok {
								walk(b)
							} else if elif, ok := s.Else.(*ast.IfStmt); ok {
								walk(&ast.BlockStmt{List: []ast.Stmt{elif}})
							}
						}
					case *ast.BlockStmt:
						walk(s)
					case *ast.ForStmt:
						walk(s.Body)
					case *ast.RangeStmt:
						walk(s.Body)
					case *ast.ExprStmt:
						inspectNamedConstructorCalls(s.X, path, fnName, "newReloadState", localCallables, &calls)
					case *ast.ReturnStmt:
						for _, r := range s.Results {
							inspectNamedConstructorCalls(r, path, fnName, "newReloadState", localCallables, &calls)
						}
					default:
						ast.Inspect(s, func(n ast.Node) bool {
							call, ok := n.(*ast.CallExpr)
							if !ok {
								return true
							}
							recordNamedConstructorCall(call, path, fnName, "newReloadState", localCallables, &calls)
							return true
						})
					}
				}
			}
			walk(fd.Body)
		}
	}

	allowed := 0
	for _, c := range calls {
		if c.direct && c.file == "coordinator.go" && c.fn == "NewCoordinator" {
			allowed++
			continue
		}
		if c.direct {
			violations = append(violations, fmt.Sprintf("%s: extra newReloadState/aliased constructor call in %s", c.file, c.fn))
			continue
		}
		violations = append(violations, fmt.Sprintf("%s: aliased constructor call %q in %s", c.file, c.name, c.fn))
	}
	if allowed != 1 {
		violations = append(violations, fmt.Sprintf("want exactly one newReloadState call in NewCoordinator; got %d allowed-site call(s)", allowed))
	}
	violations = append(violations, analyzeReloadStateAllocations(files)...)
	return violations
}

func collectReloadStateConcreteNames(files map[string]*ast.File) map[string]bool {
	names := map[string]bool{"ReloadState": true}
	aliases := collectPackageTypeAliases(files)
	for name, under := range aliases {
		u := strings.TrimPrefix(under, "*")
		if u == "ReloadState" {
			names[name] = true
		}
	}
	return names
}

func typeExprIsReloadStateConcrete(expr ast.Expr, names map[string]bool) bool {
	switch e := unwrapParen(expr).(type) {
	case *ast.Ident:
		return names[e.Name]
	case *ast.StarExpr:
		return typeExprIsReloadStateConcrete(e.X, names)
	default:
		return false
	}
}

func typeExprIsConcreteReloadStateValue(expr ast.Expr, names map[string]bool) bool {
	id, ok := unwrapParen(expr).(*ast.Ident)
	return ok && names[id.Name]
}

func analyzeReloadStateAllocations(files map[string]*ast.File) []string {
	var violations []string
	names := collectReloadStateConcreteNames(files)

	type allocSite struct {
		path string
		fn   string
		kind string
	}
	var sites []allocSite
	record := func(path, fn, kind string) {
		sites = append(sites, allocSite{path: path, fn: fn, kind: kind})
	}

	inspectAllocExpr := func(expr ast.Expr, path, fn string) {
		if expr == nil {
			return
		}
		ast.Inspect(expr, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.UnaryExpr:
				if x.Op != token.AND {
					return true
				}
				if lit, ok := unwrapParen(x.X).(*ast.CompositeLit); ok && lit.Type != nil && typeExprIsConcreteReloadStateValue(lit.Type, names) {
					record(path, fn, "composite allocation &T{}")
					return false
				}
			case *ast.CompositeLit:
				if x.Type != nil && typeExprIsConcreteReloadStateValue(x.Type, names) {
					record(path, fn, "composite allocation T{}")
					return false
				}
			case *ast.CallExpr:
				id, ok := unwrapParen(x.Fun).(*ast.Ident)
				if !ok || id.Name != "new" || len(x.Args) != 1 {
					return true
				}
				if typeExprIsConcreteReloadStateValue(x.Args[0], names) {
					record(path, fn, "new(T) allocation")
					return false
				}
			}
			return true
		})
	}

	inspectValueSpec := func(vs *ast.ValueSpec, path, fn string, packageScope bool) {
		if vs.Type != nil {
			if typeExprIsConcreteReloadStateValue(vs.Type, names) {
				record(path, fn, "zero-value concrete variable")
			} else if packageScope && typeExprIsReloadStateConcrete(vs.Type, names) {
				record(path, fn, "package-scope instance variable")
			}
		}
		for _, v := range vs.Values {
			inspectAllocExpr(v, path, fn)
		}
	}

	for path, file := range files {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					continue
				}
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					inspectValueSpec(vs, path, "", true)
				}
			case *ast.FuncDecl:
				if d.Body == nil {
					continue
				}
				fnName := ""
				if d.Name != nil {
					fnName = d.Name.Name
				}
				ast.Inspect(d.Body, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.ValueSpec:
						inspectValueSpec(x, path, fnName, false)
						return false
					case *ast.AssignStmt:
						for _, rhs := range x.Rhs {
							inspectAllocExpr(rhs, path, fnName)
						}
						return false
					case *ast.ReturnStmt:
						for _, r := range x.Results {
							inspectAllocExpr(r, path, fnName)
						}
						return false
					case *ast.CallExpr:
						inspectAllocExpr(x, path, fnName)
						return false
					case *ast.UnaryExpr, *ast.CompositeLit:
						inspectAllocExpr(x.(ast.Expr), path, fnName)
						return false
					}
					return true
				})
			}
		}
	}

	if len(sites) != 1 {
		violations = append(violations, fmt.Sprintf("want exactly one concrete ReloadState allocation site total; got %d", len(sites)))
	}
	for _, s := range sites {
		if s.path == "reload_state.go" && s.fn == "newReloadState" {
			continue
		}
		if s.fn == "" {
			violations = append(violations, fmt.Sprintf("%s: %s of ReloadState outside newReloadState", s.path, s.kind))
			continue
		}
		violations = append(violations, fmt.Sprintf("%s: %s of ReloadState in %s (only newReloadState may allocate)", s.path, s.kind, s.fn))
	}
	if len(sites) == 1 {
		s := sites[0]
		if !(s.path == "reload_state.go" && s.fn == "newReloadState") {
			violations = append(violations, fmt.Sprintf("%s: sole ReloadState allocation must be inside newReloadState in reload_state.go; found in %s", s.path, s.fn))
		}
	} else if len(sites) > 1 {
		canonical := 0
		for _, s := range sites {
			if s.path == "reload_state.go" && s.fn == "newReloadState" {
				canonical++
			}
		}
		if canonical > 1 {
			violations = append(violations, fmt.Sprintf("reload_state.go: newReloadState must contain exactly one concrete ReloadState allocation; got %d", canonical))
		}
	}
	return violations
}

type reloadStateValueProv int

const (
	stateProvUnknown reloadStateValueProv = iota
	stateProvReloadState
)

type reloadStateCallSite struct {
	file string
	fn   string
}

// findReloadStateMethodSites proves method receivers through typed *ReloadState
// params/receivers, exact typed Coordinator.state, and transitive local aliases.
// Method-value aliases are reported separately and rejected.
func findReloadStateMethodSites(files map[string]*ast.File, method string) (sites []reloadStateCallSite, methodValueViolations []string) {
	aliases := collectPackageTypeAliases(files)
	fields := collectStructFieldTypes(files, aliases)

	for path, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fn := funcDisplayName(fd)
			env := map[string]reloadStateValueProv{}
			if fd.Recv != nil {
				for _, f := range fd.Recv.List {
					if provenanceFromReloadStateType(f.Type, aliases) == stateProvReloadState {
						for _, n := range f.Names {
							if n != nil {
								env[n.Name] = stateProvReloadState
							}
						}
					}
				}
			}
			if fd.Type != nil && fd.Type.Params != nil {
				for _, f := range fd.Type.Params.List {
					if provenanceFromReloadStateType(f.Type, aliases) == stateProvReloadState {
						for _, n := range f.Names {
							if n != nil {
								env[n.Name] = stateProvReloadState
							}
						}
					}
				}
			}
			s, mv := collectReloadStateMethodSites(path, fn, method, fd.Body, env, aliases, fields)
			sites = append(sites, s...)
			methodValueViolations = append(methodValueViolations, mv...)
		}
	}
	return sites, methodValueViolations
}

func provenanceFromReloadStateType(expr ast.Expr, aliases map[string]string) reloadStateValueProv {
	under := resolveTypeString(expr, aliases)
	if under == "*ReloadState" || under == "ReloadState" {
		return stateProvReloadState
	}
	return stateProvUnknown
}

func reloadStateExprProvenance(expr ast.Expr, env map[string]reloadStateValueProv, fields map[string]map[string]string) reloadStateValueProv {
	switch e := unwrapParen(expr).(type) {
	case *ast.Ident:
		if p, ok := env[e.Name]; ok {
			return p
		}
	case *ast.SelectorExpr:
		if e.Sel == nil || e.Sel.Name != "state" {
			return stateProvUnknown
		}
		recv, ok := e.X.(*ast.Ident)
		if !ok || recv.Name != "c" {
			return stateProvUnknown
		}
		if ft := fields["Coordinator"]["state"]; ft == "*ReloadState" || ft == "ReloadState" {
			return stateProvReloadState
		}
	}
	return stateProvUnknown
}

func collectReloadStateMethodSites(
	path, fn, method string,
	body *ast.BlockStmt,
	env map[string]reloadStateValueProv,
	aliases map[string]string,
	fields map[string]map[string]string,
) (sites []reloadStateCallSite, methodValueViolations []string) {
	var walk func(body *ast.BlockStmt, env map[string]reloadStateValueProv)

	recordMethodValue := func(sel *ast.SelectorExpr, env map[string]reloadStateValueProv) {
		if sel == nil || sel.Sel == nil || sel.Sel.Name != method {
			return
		}
		if reloadStateExprProvenance(sel.X, env, fields) != stateProvReloadState {
			return
		}
		methodValueViolations = append(methodValueViolations, fmt.Sprintf("%s: method-value alias of %s in %s", path, method, fn))
	}
	recordCall := func(call *ast.CallExpr, env map[string]reloadStateValueProv) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != method {
			return
		}
		if reloadStateExprProvenance(sel.X, env, fields) == stateProvReloadState {
			sites = append(sites, reloadStateCallSite{file: path, fn: fn})
		}
	}
	inspectExpr := func(expr ast.Expr, env map[string]reloadStateValueProv) {
		if expr == nil {
			return
		}
		ast.Inspect(expr, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				recordCall(call, env)
			}
			return true
		})
	}
	applyAssign := func(as *ast.AssignStmt, env map[string]reloadStateValueProv) {
		for _, rhs := range as.Rhs {
			if sel, ok := unwrapParen(rhs).(*ast.SelectorExpr); ok {
				recordMethodValue(sel, env)
			}
			inspectExpr(rhs, env)
		}
		for i, rhs := range as.Rhs {
			if i >= len(as.Lhs) {
				continue
			}
			lhs, ok := as.Lhs[i].(*ast.Ident)
			if !ok || lhs.Name == "_" {
				continue
			}
			if p := reloadStateExprProvenance(rhs, env, fields); p != stateProvUnknown {
				env[lhs.Name] = p
			}
		}
	}
	applyValueSpec := func(vs *ast.ValueSpec, env map[string]reloadStateValueProv) {
		if vs.Type != nil {
			if p := provenanceFromReloadStateType(vs.Type, aliases); p != stateProvUnknown {
				for _, name := range vs.Names {
					if name != nil {
						env[name.Name] = p
					}
				}
			}
		}
		for i, name := range vs.Names {
			if name == nil || i >= len(vs.Values) {
				continue
			}
			if sel, ok := unwrapParen(vs.Values[i]).(*ast.SelectorExpr); ok {
				recordMethodValue(sel, env)
			}
			inspectExpr(vs.Values[i], env)
			if p := reloadStateExprProvenance(vs.Values[i], env, fields); p != stateProvUnknown {
				env[name.Name] = p
			}
		}
	}
	walk = func(body *ast.BlockStmt, env map[string]reloadStateValueProv) {
		if body == nil {
			return
		}
		for _, stmt := range body.List {
			switch s := stmt.(type) {
			case *ast.AssignStmt:
				applyAssign(s, env)
			case *ast.DeclStmt:
				gd, ok := s.Decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gd.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						applyValueSpec(vs, env)
					}
				}
			case *ast.DeferStmt:
				if s.Call == nil {
					continue
				}
				if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
					child := cloneReloadStateProv(env)
					if lit.Type != nil && lit.Type.Params != nil {
						for _, f := range lit.Type.Params.List {
							if provenanceFromReloadStateType(f.Type, aliases) == stateProvReloadState {
								for _, n := range f.Names {
									if n != nil {
										child[n.Name] = stateProvReloadState
									}
								}
							}
						}
					}
					walk(lit.Body, child)
					continue
				}
				inspectExpr(s.Call, env)
			case *ast.GoStmt:
				if s.Call == nil {
					continue
				}
				if lit, ok := s.Call.Fun.(*ast.FuncLit); ok {
					walk(lit.Body, cloneReloadStateProv(env))
					continue
				}
				inspectExpr(s.Call, env)
			case *ast.ExprStmt:
				if sel, ok := unwrapParen(s.X).(*ast.SelectorExpr); ok {
					recordMethodValue(sel, env)
				}
				inspectExpr(s.X, env)
			case *ast.IfStmt:
				child := cloneReloadStateProv(env)
				if s.Init != nil {
					if as, ok := s.Init.(*ast.AssignStmt); ok {
						applyAssign(as, child)
					}
				}
				inspectExpr(s.Cond, child)
				walk(s.Body, child)
				if s.Else != nil {
					elseEnv := cloneReloadStateProv(env)
					switch e := s.Else.(type) {
					case *ast.BlockStmt:
						walk(e, elseEnv)
					case *ast.IfStmt:
						walk(&ast.BlockStmt{List: []ast.Stmt{e}}, elseEnv)
					}
				}
			case *ast.BlockStmt:
				walk(s, cloneReloadStateProv(env))
			case *ast.ForStmt:
				child := cloneReloadStateProv(env)
				if s.Init != nil {
					if as, ok := s.Init.(*ast.AssignStmt); ok {
						applyAssign(as, child)
					}
				}
				walk(s.Body, child)
			case *ast.RangeStmt:
				walk(s.Body, cloneReloadStateProv(env))
			case *ast.ReturnStmt:
				for _, r := range s.Results {
					inspectExpr(r, env)
				}
			default:
				ast.Inspect(s, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.CallExpr:
						recordCall(x, env)
					case *ast.AssignStmt:
						applyAssign(x, env)
						return false
					}
					return true
				})
			}
		}
	}
	walk(body, env)
	return sites, methodValueViolations
}

func cloneReloadStateProv(in map[string]reloadStateValueProv) map[string]reloadStateValueProv {
	out := make(map[string]reloadStateValueProv, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// scanObserverForHistoryOwnership rejects ReloadObserver/ReloadObserverDeps
// storage or mutation of StatusHistory or canonical HistoryEntry collections,
// including nested wrappers, package globals, aliases, and renamed accessors.
// Any package-global StatusHistory or canonical HistoryEntry collection in
// runtimehost is permanently rejected: ReloadState owns bounded history as an
// instance field, never as a package global.
func scanObserverForHistoryOwnership(files map[string]*ast.File) []string {
	var violations []string
	graph := buildPackageTypeGraph(files)
	observerFields := map[string]map[string]ast.Expr{} // type -> field -> type expr
	historyGlobals := map[string]string{}              // package var -> resolved typ
	historyGlobalPath := map[string]string{}           // package var -> declaring file
	freeFuncResults := map[string]string{}             // free func -> first result type

	for _, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Recv != nil {
				continue
			}
			if fd.Type == nil || fd.Type.Results == nil || len(fd.Type.Results.List) == 0 {
				continue
			}
			freeFuncResults[fd.Name.Name] = resolveTypeString(fd.Type.Results.List[0].Type, graph.aliases)
		}
	}

	collectHistoryGlobals := func() {
		for path, file := range files {
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					var typ string
					if vs.Type != nil {
						typ = resolveTypeString(vs.Type, graph.aliases)
					}
					for i, name := range vs.Names {
						if name == nil {
							continue
						}
						t := typ
						if t == "" && i < len(vs.Values) {
							t = inferCanonicalHistoryStorageType(vs.Values[i], graph, historyGlobals, freeFuncResults)
						}
						if isCanonicalHistoryCollection(t) || isStatusHistoryType(t) {
							historyGlobals[name.Name] = t
							historyGlobalPath[name.Name] = path
						}
					}
				}
			}
		}
	}
	// Fixed-point for global alias chains (var a = b).
	for range 8 {
		before := len(historyGlobals)
		collectHistoryGlobals()
		if len(historyGlobals) == before {
			break
		}
	}

	for name, typ := range historyGlobals {
		path := historyGlobalPath[name]
		violations = append(violations, fmt.Sprintf("%s: package-global canonical history storage %q typed %s (ReloadState instance field is the sole owner)", path, name, typ))
	}

	for path, file := range files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			switch gd.Tok {
			case token.TYPE:
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name == nil {
						continue
					}
					if ts.Name.Name != "ReloadObserver" && ts.Name.Name != "ReloadObserverDeps" {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil {
						continue
					}
					fields := map[string]ast.Expr{}
					for _, f := range st.Fields.List {
						if typeOwnsCanonicalHistory(f.Type, graph, nil) {
							typ := resolveTypeString(f.Type, graph.aliases)
							violations = append(violations, fmt.Sprintf("%s: %s must not declare canonical history storage typed %s", path, ts.Name.Name, typ))
						}
						for _, n := range f.Names {
							if n != nil {
								fields[n.Name] = f.Type
							}
						}
						// Embedded fields contribute under their type base name.
						if len(f.Names) == 0 {
							base := namedTypeBase(resolveTypeString(f.Type, graph.aliases))
							if base != "" {
								fields[base] = f.Type
							}
						}
					}
					observerFields[ts.Name.Name] = fields
				}
			}
		}
	}

	for path, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil {
				continue
			}
			if fd.Recv == nil || len(fd.Recv.List) == 0 {
				continue
			}
			recv := strings.TrimPrefix(resolveTypeString(fd.Recv.List[0].Type, graph.aliases), "*")
			if recv != "ReloadObserver" && recv != "ReloadObserverDeps" {
				continue
			}
			if fd.Type != nil && fd.Type.Results != nil {
				for _, r := range fd.Type.Results.List {
					typ := resolveTypeString(r.Type, graph.aliases)
					if isCanonicalHistoryCollection(typ) || isStatusHistoryType(typ) {
						violations = append(violations, fmt.Sprintf("%s: %s.%s exposes canonical history collection typed %s", path, recv, fd.Name.Name, typ))
					}
				}
			}
			if fd.Body == nil {
				continue
			}
			recvNames := map[string]bool{}
			for _, n := range fd.Recv.List[0].Names {
				if n != nil {
					recvNames[n.Name] = true
				}
			}
			fieldExprs := observerFields[recv]
			env := map[string]string{} // local aliases of history collections
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.AssignStmt:
					for i, rhs := range x.Rhs {
						typ := historyCollectionExprType(rhs, recvNames, fieldExprs, env, historyGlobals, graph)
						if typ == "" {
							continue
						}
						if i < len(x.Lhs) {
							switch lhs := x.Lhs[i].(type) {
							case *ast.Ident:
								if lhs.Name != "_" {
									env[lhs.Name] = typ
								}
							default:
								// Assignment into nested field / global is mutation.
								if historyCollectionExprType(x.Lhs[i], recvNames, fieldExprs, env, historyGlobals, graph) != "" {
									violations = append(violations, fmt.Sprintf("%s: %s.%s mutates canonical history collection via assignment", path, recv, fd.Name.Name))
								}
							}
						}
					}
					// Detect append assigned back: local = append(local, ...)
					for _, rhs := range x.Rhs {
						call, ok := unwrapParen(rhs).(*ast.CallExpr)
						if !ok {
							continue
						}
						id, ok := unwrapParen(call.Fun).(*ast.Ident)
						if !ok || id.Name != "append" || len(call.Args) < 1 {
							continue
						}
						if typ := historyCollectionExprType(call.Args[0], recvNames, fieldExprs, env, historyGlobals, graph); typ != "" {
							violations = append(violations, fmt.Sprintf("%s: %s.%s mutates canonical history collection via append", path, recv, fd.Name.Name))
						}
					}
				case *ast.CallExpr:
					id, ok := unwrapParen(x.Fun).(*ast.Ident)
					if !ok || id.Name != "append" || len(x.Args) < 1 {
						return true
					}
					if typ := historyCollectionExprType(x.Args[0], recvNames, fieldExprs, env, historyGlobals, graph); typ != "" {
						violations = append(violations, fmt.Sprintf("%s: %s.%s mutates canonical history collection via append", path, recv, fd.Name.Name))
					}
				case *ast.ReturnStmt:
					for _, r := range x.Results {
						if typ := historyCollectionExprType(r, recvNames, fieldExprs, env, historyGlobals, graph); typ != "" {
							violations = append(violations, fmt.Sprintf("%s: %s.%s exposes canonical history collection via return", path, recv, fd.Name.Name))
						}
					}
				}
				return true
			})
		}
	}
	return violations
}

// inferCanonicalHistoryStorageType resolves explicit types, aliases/defined
// types, T{}, make(T, ...), new(T), constructor calls with declared StatusHistory
// / HistoryEntry-collection results, and straightforward global aliases.
func inferCanonicalHistoryStorageType(
	expr ast.Expr,
	graph *packageTypeGraph,
	globals map[string]string,
	freeFuncResults map[string]string,
) string {
	switch e := unwrapParen(expr).(type) {
	case *ast.Ident:
		if typ, ok := globals[e.Name]; ok {
			return typ
		}
	case *ast.CompositeLit:
		if e.Type != nil {
			return resolveTypeString(e.Type, graph.aliases)
		}
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			t := inferCanonicalHistoryStorageType(e.X, graph, globals, freeFuncResults)
			if t != "" && !strings.HasPrefix(t, "*") {
				t = "*" + t
			}
			return t
		}
	case *ast.CallExpr:
		if id, ok := unwrapParen(e.Fun).(*ast.Ident); ok {
			switch {
			case id.Name == "make" && len(e.Args) >= 1:
				return resolveTypeString(e.Args[0], graph.aliases)
			case id.Name == "new" && len(e.Args) == 1:
				t := resolveTypeString(e.Args[0], graph.aliases)
				if t != "" && !strings.HasPrefix(t, "*") {
					t = "*" + t
				}
				return t
			default:
				if t, ok := freeFuncResults[id.Name]; ok {
					return t
				}
			}
		}
	}
	return ""
}

func inferHistoryCollectionFromComposite(expr ast.Expr, graph *packageTypeGraph) string {
	return inferCanonicalHistoryStorageType(expr, graph, nil, nil)
}

func typeOwnsCanonicalHistory(expr ast.Expr, graph *packageTypeGraph, visiting map[string]bool) bool {
	if expr == nil {
		return false
	}
	if visiting == nil {
		visiting = map[string]bool{}
	}
	expr = unwrapParen(expr)
	switch e := expr.(type) {
	case *ast.StarExpr:
		return typeOwnsCanonicalHistory(e.X, graph, visiting)
	case *ast.ArrayType:
		typ := resolveTypeString(e, graph.aliases)
		return isCanonicalHistoryCollection(typ) || isStatusHistoryType(typ)
	case *ast.MapType:
		typ := resolveTypeString(e, graph.aliases)
		return isCanonicalHistoryCollection(typ) || isStatusHistoryType(typ)
	case *ast.StructType:
		if e.Fields == nil {
			return false
		}
		for _, f := range e.Fields.List {
			if typeOwnsCanonicalHistory(f.Type, graph, visiting) {
				return true
			}
		}
		return false
	}
	typ := resolveTypeString(expr, graph.aliases)
	if isCanonicalHistoryCollection(typ) || isStatusHistoryType(typ) {
		return true
	}
	base := namedTypeBase(typ)
	if base == "" || visiting[base] {
		return false
	}
	st, ok := graph.structs[base]
	if !ok {
		return false
	}
	visiting[base] = true
	defer delete(visiting, base)
	return typeOwnsCanonicalHistory(st, graph, visiting)
}

func historyCollectionExprType(
	expr ast.Expr,
	recvNames map[string]bool,
	fieldExprs map[string]ast.Expr,
	env map[string]string,
	globals map[string]string,
	graph *packageTypeGraph,
) string {
	switch e := unwrapParen(expr).(type) {
	case *ast.Ident:
		if typ, ok := env[e.Name]; ok && (isCanonicalHistoryCollection(typ) || isStatusHistoryType(typ)) {
			return typ
		}
		if typ, ok := globals[e.Name]; ok {
			return typ
		}
	case *ast.SelectorExpr:
		if e.Sel == nil {
			return ""
		}
		// Direct recv.field
		if recv, ok := e.X.(*ast.Ident); ok && recvNames[recv.Name] {
			if ft, ok := fieldExprs[e.Sel.Name]; ok {
				if typeOwnsCanonicalHistory(ft, graph, nil) {
					typ := resolveTypeString(ft, graph.aliases)
					if isCanonicalHistoryCollection(typ) || isStatusHistoryType(typ) {
						return typ
					}
					// Nested wrapper: resolve leaf history type if present.
					return nestedHistoryLeafType(ft, graph, nil)
				}
			}
		}
		// Nested recv.box.events — walk selector chain from receiver.
		if leaf := selectorHistoryFromRecv(e, recvNames, fieldExprs, graph); leaf != "" {
			return leaf
		}
	}
	return ""
}

func nestedHistoryLeafType(expr ast.Expr, graph *packageTypeGraph, visiting map[string]bool) string {
	if expr == nil {
		return ""
	}
	if visiting == nil {
		visiting = map[string]bool{}
	}
	typ := resolveTypeString(expr, graph.aliases)
	if isCanonicalHistoryCollection(typ) || isStatusHistoryType(typ) {
		return typ
	}
	base := namedTypeBase(typ)
	if base == "" || visiting[base] {
		return ""
	}
	st, ok := graph.structs[base]
	if !ok || st.Fields == nil {
		return ""
	}
	visiting[base] = true
	defer delete(visiting, base)
	for _, f := range st.Fields.List {
		if got := nestedHistoryLeafType(f.Type, graph, visiting); got != "" {
			return got
		}
	}
	return ""
}

func selectorHistoryFromRecv(
	sel *ast.SelectorExpr,
	recvNames map[string]bool,
	fieldExprs map[string]ast.Expr,
	graph *packageTypeGraph,
) string {
	// Flatten sel chain into [recv, f1, f2, ...]
	var parts []string
	var cur ast.Expr = sel
	for {
		switch x := unwrapParen(cur).(type) {
		case *ast.SelectorExpr:
			if x.Sel == nil {
				return ""
			}
			parts = append([]string{x.Sel.Name}, parts...)
			cur = x.X
		case *ast.Ident:
			if !recvNames[x.Name] {
				return ""
			}
			// parts now holds field path from receiver.
			if len(parts) == 0 {
				return ""
			}
			ft, ok := fieldExprs[parts[0]]
			if !ok {
				return ""
			}
			// Walk remaining nested fields through the type graph.
			current := ft
			for i := 1; i < len(parts); i++ {
				base := namedTypeBase(resolveTypeString(current, graph.aliases))
				st, ok := graph.structs[base]
				if !ok || st.Fields == nil {
					return ""
				}
				next, ok := structFieldTypeExpr(st, parts[i])
				if !ok {
					return ""
				}
				current = next
			}
			typ := resolveTypeString(current, graph.aliases)
			if isCanonicalHistoryCollection(typ) || isStatusHistoryType(typ) {
				return typ
			}
			if typeOwnsCanonicalHistory(current, graph, nil) {
				return nestedHistoryLeafType(current, graph, nil)
			}
			return ""
		default:
			return ""
		}
	}
}

func structFieldTypeExpr(st *ast.StructType, name string) (ast.Expr, bool) {
	if st == nil || st.Fields == nil {
		return nil, false
	}
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n != nil && n.Name == name {
				return f.Type, true
			}
		}
		if len(f.Names) == 0 {
			base := namedTypeBase(typeExprString(f.Type))
			if base == name {
				return f.Type, true
			}
		}
	}
	return nil, false
}

var forbiddenReloadStateCollaborators = map[string]bool{
	"Manager":            true,
	"*Manager":           true,
	"ReloadObserver":     true,
	"*ReloadObserver":    true,
	"attemptGate":        true,
	"*attemptGate":       true,
	"attemptLease":       true,
	"*attemptLease":      true,
	"attemptRunner":      true,
	"*attemptRunner":     true,
	"CandidateCompiler":  true,
	"EffectiveLoader":    true,
	"StableConfigSource": true,
	"Coordinator":        true,
	"*Coordinator":       true,
}

var forbiddenReloadStateImports = []string{
	"log/slog",
	"net/http",
	"go.opentelemetry.io/otel/trace",
	"go.opentelemetry.io/otel/attribute",
	"go.opentelemetry.io/otel/codes",
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics",
}

// scanReloadStateForbiddenDependencies rejects forbidden collaborator storage
// under aliases/defined types/nested wrappers across ReloadState ownership,
// function-typed callback/hook fields, ReloadState method collaborator or
// callback use, and hook-registry types only when declared in reload_state.go
// or owned/referenced by ReloadState.
func scanReloadStateForbiddenDependencies(files map[string]*ast.File) []string {
	var violations []string
	graph := buildPackageTypeGraph(files)

	stateNames := map[string]bool{"ReloadState": true}
	inputNames := map[string]bool{
		"ReloadState": true, "reloadStateInitial": true,
		"reloadTerminalMeta": true, "reloadStatusInput": true,
	}
	for name, under := range graph.aliases {
		u := strings.TrimPrefix(under, "*")
		if u == "ReloadState" {
			stateNames[name] = true
			inputNames[name] = true
		}
		if u == "reloadStateInitial" || u == "reloadTerminalMeta" || u == "reloadStatusInput" {
			inputNames[name] = true
		}
	}
	// Collect hook-registry type names referenced by ReloadState ownership.
	ownedHookRegistries := map[string]bool{}
	for name := range inputNames {
		st, ok := graph.structs[name]
		if !ok || st.Fields == nil {
			continue
		}
		collectHookRegistryRefs(st, graph, ownedHookRegistries, nil)
	}

	for path, file := range files {
		if path == "reload_state.go" {
			for _, imp := range file.Imports {
				ipath := strings.Trim(imp.Path.Value, `"`)
				for _, bad := range forbiddenReloadStateImports {
					if ipath == bad {
						violations = append(violations, fmt.Sprintf("reload_state.go: forbidden import %q", ipath))
					}
				}
			}
		}
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil {
					continue
				}
				if isHookRegistryName(ts.Name.Name) {
					inReloadStateFile := path == "reload_state.go"
					if inReloadStateFile || ownedHookRegistries[ts.Name.Name] {
						violations = append(violations, fmt.Sprintf("%s: unexpected generic hook registry type %q", path, ts.Name.Name))
					}
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				scanOwned := stateNames[ts.Name.Name] || inputNames[ts.Name.Name]
				if !scanOwned {
					continue
				}
				for _, f := range st.Fields.List {
					if bad := typeExprContainsForbiddenCollaborator(f.Type, graph, nil); bad != "" && bad != "func" {
						fname := ts.Name.Name
						if len(f.Names) > 0 {
							fname = ts.Name.Name + "." + f.Names[0].Name
						}
						violations = append(violations, fmt.Sprintf("%s: %s stores forbidden collaborator typed %s", path, fname, bad))
					}
					if typeExprContainsFuncCallback(f.Type, graph, nil) {
						fname := "(embedded)"
						if len(f.Names) > 0 {
							fname = f.Names[0].Name
						}
						violations = append(violations, fmt.Sprintf("%s: %s.%s is a forbidden function-typed callback/hook field", path, ts.Name.Name, fname))
					}
					if isHookRegistryName(namedTypeBase(resolveTypeString(f.Type, graph.aliases))) {
						ownedHookRegistries[namedTypeBase(resolveTypeString(f.Type, graph.aliases))] = true
						base := namedTypeBase(resolveTypeString(f.Type, graph.aliases))
						violations = append(violations, fmt.Sprintf("%s: %s stores forbidden hook registry typed %s", path, ts.Name.Name, base))
					}
				}
			}
		}
	}

	violations = append(violations, scanReloadStateMethodDependencies(files, graph, stateNames)...)
	return violations
}

func collectHookRegistryRefs(st *ast.StructType, graph *packageTypeGraph, out map[string]bool, visiting map[string]bool) {
	if st == nil || st.Fields == nil {
		return
	}
	if visiting == nil {
		visiting = map[string]bool{}
	}
	for _, f := range st.Fields.List {
		base := namedTypeBase(resolveTypeString(f.Type, graph.aliases))
		if base == "" {
			continue
		}
		if isHookRegistryName(base) {
			out[base] = true
		}
		if visiting[base] {
			continue
		}
		child, ok := graph.structs[base]
		if !ok {
			continue
		}
		visiting[base] = true
		collectHookRegistryRefs(child, graph, out, visiting)
		delete(visiting, base)
	}
}

func scanReloadStateMethodDependencies(files map[string]*ast.File, graph *packageTypeGraph, stateNames map[string]bool) []string {
	aliases := graph.aliases
	infos := map[packageFnID]*ast.FuncDecl{}
	infoPath := map[packageFnID]string{}
	freeFuncs := map[string]packageFnID{}
	methods := map[string]map[string]packageFnID{}
	resultTypes := map[packageFnID][]ast.Expr{} // declared result type exprs
	globals := map[string]string{}              // package var -> resolved type
	globalFuncs := map[string]bool{}            // package var holding func value

	for path, file := range files {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Name == nil || d.Body == nil {
					continue
				}
				id, recvType := packageFnIdentity(d, aliases)
				infos[id] = d
				infoPath[id] = path
				if recvType == "" {
					freeFuncs[d.Name.Name] = id
				} else {
					if methods[recvType] == nil {
						methods[recvType] = map[string]packageFnID{}
					}
					methods[recvType][d.Name.Name] = id
				}
				if d.Type != nil && d.Type.Results != nil {
					var rts []ast.Expr
					for _, r := range d.Type.Results.List {
						n := len(r.Names)
						if n == 0 {
							n = 1
						}
						for i := 0; i < n; i++ {
							rts = append(rts, r.Type)
						}
					}
					resultTypes[id] = rts
				}
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					continue
				}
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					var typ string
					var isFunc bool
					if vs.Type != nil {
						typ = resolveTypeString(vs.Type, aliases)
						isFunc = isFuncTypedExpr(vs.Type, graph)
					}
					for i, name := range vs.Names {
						if name == nil {
							continue
						}
						t := typ
						f := isFunc
						if t == "" && i < len(vs.Values) {
							t, f = inferExprTypeAndFunc(vs.Values[i], globals, globalFuncs, graph, freeFuncs, resultTypes, nil)
						}
						if t != "" {
							globals[name.Name] = t
						}
						if f {
							globalFuncs[name.Name] = true
						}
					}
				}
			}
		}
	}
	// Fixed-point global alias chains (var a = b where b is known).
	for range 8 {
		changed := false
		for path, file := range files {
			_ = path
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vs.Names {
						if name == nil || globals[name.Name] != "" || i >= len(vs.Values) {
							continue
						}
						t, f := inferExprTypeAndFunc(vs.Values[i], globals, globalFuncs, graph, freeFuncs, resultTypes, nil)
						if t != "" {
							globals[name.Name] = t
							changed = true
						}
						if f && !globalFuncs[name.Name] {
							globalFuncs[name.Name] = true
							changed = true
						}
					}
				}
			}
		}
		if !changed {
			break
		}
	}

	calleesOf := map[packageFnID][]packageFnID{}
	for id, fd := range infos {
		recvTypes := map[string]string{}
		funcAlias := map[string]packageFnID{}
		methodAlias := map[string]packageFnID{}
		if fd.Recv != nil {
			for _, f := range fd.Recv.List {
				typ := strings.TrimPrefix(resolveTypeString(f.Type, aliases), "*")
				for _, n := range f.Names {
					if n != nil {
						recvTypes[n.Name] = typ
					}
				}
			}
		}
		if fd.Type != nil && fd.Type.Params != nil {
			for _, f := range fd.Type.Params.List {
				under := strings.TrimPrefix(resolveTypeString(f.Type, aliases), "*")
				if under == "" {
					continue
				}
				for _, n := range f.Names {
					if n != nil {
						recvTypes[n.Name] = under
					}
				}
			}
		}
		calleesOf[id] = collectPackageCallees(fd.Body, freeFuncs, methods, recvTypes, funcAlias, methodAlias, aliases)
	}

	// Roots: every production method on resolved ReloadState (including aliases).
	roots := []packageFnID{}
	for id, fd := range infos {
		if fd.Recv == nil || len(fd.Recv.List) == 0 {
			continue
		}
		recv := strings.TrimPrefix(resolveTypeString(fd.Recv.List[0].Type, aliases), "*")
		if stateNames[recv] {
			roots = append(roots, id)
		}
	}

	reachable := map[packageFnID]bool{}
	queue := append([]packageFnID{}, roots...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if reachable[id] {
			continue
		}
		reachable[id] = true
		for _, c := range calleesOf[id] {
			if !reachable[c] {
				queue = append(queue, c)
			}
		}
	}

	var violations []string
	for id := range reachable {
		fd := infos[id]
		path := infoPath[id]
		label := reloadStateReachableLabel(id, stateNames)
		violations = append(violations, scanReloadStateReachableFunc(
			path, label, fd, graph, globals, globalFuncs, freeFuncs, resultTypes,
		)...)
	}
	return violations
}

func reloadStateReachableLabel(id packageFnID, stateNames map[string]bool) string {
	s := string(id)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		recv := s[:i]
		if stateNames[recv] {
			return "ReloadState." + s[i+1:]
		}
	}
	return "helper " + s + " reachable from ReloadState"
}

// scanReloadStateReachableFunc applies collaborator/callback checks to one
// ReloadState method or transitively reachable package-local helper.
func scanReloadStateReachableFunc(
	path, label string,
	fd *ast.FuncDecl,
	graph *packageTypeGraph,
	globals map[string]string,
	globalFuncs map[string]bool,
	freeFuncs map[string]packageFnID,
	resultTypes map[packageFnID][]ast.Expr,
) []string {
	var violations []string
	env := map[string]string{}
	funcEnv := map[string]bool{}
	// Seed package globals into the local env so selector/refs resolve.
	for k, v := range globals {
		env[k] = v
	}
	for k, v := range globalFuncs {
		if v {
			funcEnv[k] = true
		}
	}

	seedFields := func(list []*ast.Field, kind string) {
		if list == nil {
			return
		}
		for _, f := range list {
			typ := resolveTypeString(f.Type, graph.aliases)
			isFunc := isFuncTypedExpr(f.Type, graph)
			if bad := typeExprContainsForbiddenCollaborator(f.Type, graph, nil); bad != "" && bad != "func" {
				for _, n := range f.Names {
					if n != nil {
						violations = append(violations, fmt.Sprintf("%s: %s %s %q has forbidden collaborator typed %s", path, label, kind, n.Name, bad))
					}
				}
				if len(f.Names) == 0 {
					violations = append(violations, fmt.Sprintf("%s: %s %s has forbidden collaborator typed %s", path, label, kind, bad))
				}
			}
			if isFunc {
				for _, n := range f.Names {
					if n != nil {
						funcEnv[n.Name] = true
						violations = append(violations, fmt.Sprintf("%s: %s %s %q is a forbidden callback/hook", path, label, kind, n.Name))
					}
				}
				if len(f.Names) == 0 {
					violations = append(violations, fmt.Sprintf("%s: %s %s is a forbidden callback/hook", path, label, kind))
				}
			}
			for _, n := range f.Names {
				if n == nil {
					continue
				}
				if typ != "" {
					env[n.Name] = typ
				}
				if isFunc {
					funcEnv[n.Name] = true
				}
			}
		}
	}
	if fd.Type != nil {
		seedFields(fd.Type.Params.List, "parameter")
		if fd.Type.Results != nil {
			seedFields(fd.Type.Results.List, "result")
		}
	}
	if fd.Body == nil {
		return violations
	}

	noteFactoryCall := func(call *ast.CallExpr) {
		id := resolvePackageCallID(call.Fun, freeFuncs, env)
		if id == "" {
			return
		}
		rts := resultTypes[id]
		for _, rt := range rts {
			if bad := typeExprContainsForbiddenCollaborator(rt, graph, nil); bad != "" && bad != "func" {
				violations = append(violations, fmt.Sprintf("%s: %s calls factory %s returning forbidden collaborator typed %s", path, label, id, bad))
				return
			}
		}
	}

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
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
				var typ string
				var isFunc bool
				if vs.Type != nil {
					typ = resolveTypeString(vs.Type, graph.aliases)
					isFunc = isFuncTypedExpr(vs.Type, graph)
					if bad := typeExprContainsForbiddenCollaborator(vs.Type, graph, nil); bad != "" && bad != "func" {
						violations = append(violations, fmt.Sprintf("%s: %s local has forbidden collaborator typed %s", path, label, bad))
					}
					if isFunc {
						violations = append(violations, fmt.Sprintf("%s: %s local is a forbidden callback/hook", path, label))
					}
				}
				for i, name := range vs.Names {
					if name == nil {
						continue
					}
					t := typ
					f := isFunc
					if t == "" && i < len(vs.Values) {
						t, f = inferExprTypeAndFunc(vs.Values[i], env, funcEnv, graph, freeFuncs, resultTypes, noteFactoryCall)
					}
					if t != "" {
						env[name.Name] = t
						if bad := forbiddenCollaboratorTypeString(t); bad != "" {
							violations = append(violations, fmt.Sprintf("%s: %s local %q has forbidden collaborator typed %s", path, label, name.Name, bad))
						}
					}
					if f {
						funcEnv[name.Name] = true
					}
				}
			}
		case *ast.AssignStmt:
			if x.Tok != token.DEFINE && x.Tok != token.ASSIGN {
				return true
			}
			// Multi-LHS from a single call: m, err := buildMgr()
			if len(x.Rhs) == 1 && len(x.Lhs) > 1 {
				if call, ok := unwrapParen(x.Rhs[0]).(*ast.CallExpr); ok {
					noteFactoryCall(call)
					types := inferCallResultTypes(call, env, funcEnv, graph, freeFuncs, resultTypes)
					for i, lhs := range x.Lhs {
						id, ok := lhs.(*ast.Ident)
						if !ok || id.Name == "_" {
							continue
						}
						if i < len(types) && types[i] != "" {
							if x.Tok == token.DEFINE || env[id.Name] == "" {
								env[id.Name] = types[i]
							}
							if bad := forbiddenCollaboratorTypeString(types[i]); bad != "" {
								violations = append(violations, fmt.Sprintf("%s: %s local %q has forbidden collaborator typed %s", path, label, id.Name, bad))
							}
						}
					}
					return true
				}
			}
			for i, rhs := range x.Rhs {
				if call, ok := unwrapParen(rhs).(*ast.CallExpr); ok {
					noteFactoryCall(call)
				}
				t, f := inferExprTypeAndFunc(rhs, env, funcEnv, graph, freeFuncs, resultTypes, noteFactoryCall)
				if i < len(x.Lhs) {
					if id, ok := x.Lhs[i].(*ast.Ident); ok && id.Name != "_" {
						if x.Tok == token.DEFINE || env[id.Name] == "" {
							if t != "" {
								env[id.Name] = t
								if bad := forbiddenCollaboratorTypeString(t); bad != "" {
									violations = append(violations, fmt.Sprintf("%s: %s local %q has forbidden collaborator typed %s", path, label, id.Name, bad))
								}
							}
						}
						// Only function-typed values (params/FuncLit/typed locals) are
						// callback invocations; package-function aliases are followed
						// via the call graph instead of being treated as hooks.
						if f {
							funcEnv[id.Name] = true
						}
					}
				}
			}
		case *ast.CallExpr:
			switch fun := unwrapParen(x.Fun).(type) {
			case *ast.Ident:
				if funcEnv[fun.Name] {
					violations = append(violations, fmt.Sprintf("%s: %s invokes forbidden callback %q", path, label, fun.Name))
				}
				noteFactoryCall(x)
			case *ast.SelectorExpr:
				if fun.X != nil {
					recvTyp := ""
					switch rx := unwrapParen(fun.X).(type) {
					case *ast.Ident:
						recvTyp = env[rx.Name]
					}
					if recvTyp != "" && isForbiddenCollaboratorType(recvTyp) {
						sel := ""
						if fun.Sel != nil {
							sel = fun.Sel.Name
						}
						violations = append(violations, fmt.Sprintf("%s: %s calls forbidden collaborator method %s.%s", path, label, recvTyp, sel))
					}
				}
			}
			if id, ok := unwrapParen(x.Fun).(*ast.Ident); ok && id.Name == "new" && len(x.Args) == 1 {
				if bad := typeExprContainsForbiddenCollaborator(x.Args[0], graph, nil); bad != "" && bad != "func" {
					violations = append(violations, fmt.Sprintf("%s: %s constructs forbidden collaborator typed %s", path, label, bad))
				}
			}
		case *ast.CompositeLit:
			if x.Type != nil {
				if bad := typeExprContainsForbiddenCollaborator(x.Type, graph, nil); bad != "" && bad != "func" {
					violations = append(violations, fmt.Sprintf("%s: %s constructs forbidden collaborator typed %s", path, label, bad))
				}
			}
		case *ast.Ident:
			// Reference to package-global forbidden collaborator (including aliases).
			if typ, ok := globals[x.Name]; ok {
				if bad := forbiddenCollaboratorTypeString(typ); bad != "" {
					violations = append(violations, fmt.Sprintf("%s: %s references package-global forbidden collaborator %q typed %s", path, label, x.Name, bad))
				}
			}
		}
		return true
	})
	return violations
}

func isForbiddenCollaboratorType(typ string) bool {
	return forbiddenCollaboratorTypeString(typ) != ""
}

func forbiddenCollaboratorTypeString(typ string) string {
	if typ == "" {
		return ""
	}
	if forbiddenReloadStateCollaborators[typ] {
		return typ
	}
	base := strings.TrimPrefix(typ, "*")
	if forbiddenReloadStateCollaborators[base] {
		return base
	}
	if forbiddenReloadStateCollaborators["*"+base] {
		return "*" + base
	}
	return ""
}

func resolvePackageCallID(fun ast.Expr, freeFuncs map[string]packageFnID, env map[string]string) packageFnID {
	switch f := unwrapParen(fun).(type) {
	case *ast.Ident:
		if id, ok := freeFuncs[f.Name]; ok {
			return id
		}
	}
	return ""
}

func inferCallResultTypes(
	call *ast.CallExpr,
	env map[string]string,
	funcEnv map[string]bool,
	graph *packageTypeGraph,
	freeFuncs map[string]packageFnID,
	resultTypes map[packageFnID][]ast.Expr,
) []string {
	if id, ok := unwrapParen(call.Fun).(*ast.Ident); ok && id.Name == "new" && len(call.Args) == 1 {
		t := resolveTypeString(call.Args[0], graph.aliases)
		if t != "" && !strings.HasPrefix(t, "*") {
			t = "*" + t
		}
		return []string{t}
	}
	pid := resolvePackageCallID(call.Fun, freeFuncs, env)
	if pid == "" {
		return nil
	}
	rts := resultTypes[pid]
	out := make([]string, len(rts))
	for i, rt := range rts {
		out[i] = resolveTypeString(rt, graph.aliases)
	}
	return out
}

func inferExprTypeAndFunc(
	expr ast.Expr,
	env map[string]string,
	funcEnv map[string]bool,
	graph *packageTypeGraph,
	freeFuncs map[string]packageFnID,
	resultTypes map[packageFnID][]ast.Expr,
	noteFactory func(*ast.CallExpr),
) (string, bool) {
	switch e := unwrapParen(expr).(type) {
	case *ast.Ident:
		if funcEnv[e.Name] {
			return env[e.Name], true
		}
		// Package function identifiers used as values are call-graph aliases,
		// not generic callback/hook values.
		return env[e.Name], false
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			t, f := inferExprTypeAndFunc(e.X, env, funcEnv, graph, freeFuncs, resultTypes, noteFactory)
			if t != "" && !strings.HasPrefix(t, "*") {
				t = "*" + t
			}
			return t, f
		}
	case *ast.CompositeLit:
		if e.Type != nil {
			return resolveTypeString(e.Type, graph.aliases), isFuncTypedExpr(e.Type, graph)
		}
	case *ast.CallExpr:
		if noteFactory != nil {
			noteFactory(e)
		}
		if id, ok := unwrapParen(e.Fun).(*ast.Ident); ok && id.Name == "new" && len(e.Args) == 1 {
			t := resolveTypeString(e.Args[0], graph.aliases)
			if t != "" && !strings.HasPrefix(t, "*") {
				t = "*" + t
			}
			return t, false
		}
		if id, ok := unwrapParen(e.Fun).(*ast.Ident); ok && id.Name == "make" && len(e.Args) >= 1 {
			return resolveTypeString(e.Args[0], graph.aliases), false
		}
		types := inferCallResultTypes(e, env, funcEnv, graph, freeFuncs, resultTypes)
		if len(types) > 0 {
			return types[0], false
		}
	case *ast.FuncLit:
		return "", true
	}
	return "", false
}

// --- synthetic evasion / negative-control fixtures ---

func TestReloadStateOwnershipScanner_SyntheticEvasions(t *testing.T) {
	t.Parallel()

	canonicalPair := map[string]string{
		"reload_state.go": `
package runtimehost
type ReloadState struct{}
func newReloadState() *ReloadState { return &ReloadState{} }
`,
		"coordinator.go": `
package runtimehost
type Coordinator struct { state *ReloadState }
func NewCoordinator() *Coordinator { return &Coordinator{state: newReloadState()} }
`,
	}

	t.Run("rejects_second_ReloadState_declaration", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": canonicalPair["reload_state.go"],
			"other.go": `
package runtimehost
type ReloadState struct{ y int }
`,
			"coordinator.go": canonicalPair["coordinator.go"],
		})
		got := analyzeReloadStateOwnership(files)
		if !violationContains(got, "ReloadState") {
			t.Fatalf("expected duplicate ReloadState rejection; got %v", got)
		}
	})

	t.Run("accepts_canonical_single_owner", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, canonicalPair)
		got := analyzeReloadStateOwnership(files)
		if len(got) > 0 {
			t.Fatalf("canonical single owner must be accepted; got %v", got)
		}
	})

	t.Run("rejects_wrapper_struct_storing_ReloadState", func(t *testing.T) {
		t.Parallel()
		src := map[string]string{}
		for k, v := range canonicalPair {
			src[k] = v
		}
		src["wrap.go"] = `
package runtimehost
type stateBag struct { state *ReloadState }
`
		files := mustParseSyntheticFiles(t, src)
		got := analyzeReloadStateOwnership(files)
		if !violationContains(got, "stateBag") {
			t.Fatalf("expected wrapper stateBag rejection; got %v", got)
		}
	})

	t.Run("rejects_newReloadState_value_alias_and_call", func(t *testing.T) {
		t.Parallel()
		src := map[string]string{}
		for k, v := range canonicalPair {
			src[k] = v
		}
		src["alias_ctor.go"] = `
package runtimehost
var makeState = newReloadState
func extra() *ReloadState { return makeState() }
`
		files := mustParseSyntheticFiles(t, src)
		got := analyzeReloadStateConstructorGraph(files)
		if !violationContains(got, "makeState") && !violationContains(got, "newReloadState") {
			t.Fatalf("expected constructor alias/extra call rejection; got %v", got)
		}
	})

	t.Run("rejects_local_constructor_alias_in_NewCoordinator", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
func newReloadState() *ReloadState { return &ReloadState{} }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { state *ReloadState }
func NewCoordinator() *Coordinator {
	ctor := newReloadState
	return &Coordinator{state: ctor()}
}
`,
		})
		got := analyzeReloadStateConstructorGraph(files)
		if !violationContains(got, "ctor") && !violationContains(got, "aliased") {
			t.Fatalf("expected local constructor alias rejection; got %v", got)
		}
	})

	t.Run("rejects_direct_ReloadState_allocations_despite_ctor_decoy", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
func newReloadState() *ReloadState { return &ReloadState{} }
func extra() *ReloadState { return &ReloadState{} }
func extra2() *ReloadState { return new(ReloadState) }
func extra3() *ReloadState { var r ReloadState; return &r }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { state *ReloadState }
func NewCoordinator() *Coordinator { return &Coordinator{state: newReloadState()} }
`,
		})
		got := analyzeReloadStateConstructorGraph(files)
		if !violationContains(got, "extra") || !violationContains(got, "extra2") || !violationContains(got, "extra3") {
			t.Fatalf("expected direct allocation forms rejected despite ctor decoy; got %v", got)
		}
	})

	t.Run("rejects_aliased_defined_type_allocations_outside_ctor", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
func newReloadState() *ReloadState { return &ReloadState{} }
type stateAlias = ReloadState
type stateDefined ReloadState
func viaAlias() *stateAlias { return &stateAlias{} }
func viaDefined() *stateDefined { return new(stateDefined) }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { state *ReloadState }
func NewCoordinator() *Coordinator { return &Coordinator{state: newReloadState()} }
`,
		})
		got := analyzeReloadStateConstructorGraph(files)
		if !violationContains(got, "viaAlias") || !violationContains(got, "viaDefined") {
			t.Fatalf("expected aliased/defined type allocation rejection; got %v", got)
		}
	})

	t.Run("rejects_two_allocations_inside_newReloadState", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
func newReloadState() *ReloadState {
	decoy := &ReloadState{}
	extra := &ReloadState{}
	_ = extra
	return decoy
}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { state *ReloadState }
func NewCoordinator() *Coordinator { return &Coordinator{state: newReloadState()} }
`,
		})
		got := analyzeReloadStateConstructorGraph(files)
		if !violationContains(got, "exactly one") {
			t.Fatalf("expected multiple allocations rejection; got %v", got)
		}
	})

	t.Run("accepts_widgetState_allocations_and_ReloadState_params", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
func newReloadState() *ReloadState { return &ReloadState{} }
func (s *ReloadState) Snapshot() {}
func useState(s *ReloadState) { _ = s }
type widgetState struct{}
func newWidget() *widgetState { return &widgetState{} }
func useWidget(w *widgetState) { var local widgetState; _ = local; _ = w }
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { state *ReloadState }
func NewCoordinator() *Coordinator { return &Coordinator{state: newReloadState()} }
`,
		})
		got := analyzeReloadStateConstructorGraph(files)
		if len(got) > 0 {
			t.Fatalf("widgetState allocations and *ReloadState params must be accepted; got %v", got)
		}
	})

	t.Run("rejects_param_receiver_Apply_helper_evasion", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
func newReloadState() *ReloadState { return &ReloadState{} }
func (s *ReloadState) Apply() {}
func (s *ReloadState) ActiveInput() {}
func (s *ReloadState) Snapshot() {}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { state *ReloadState }
func NewCoordinator() *Coordinator { return &Coordinator{state: newReloadState()} }
func (c *Coordinator) Reload() { c.state.ActiveInput(); c.state.Apply(); c.state.ActiveInput(); c.state.Apply() }
func (c *Coordinator) Status() { c.state.Snapshot() }
`,
			"extra.go": `
package runtimehost
func extra(s *ReloadState) { s.Apply() }
`,
		})
		sites, _ := findReloadStateMethodSites(files, "Apply")
		bad := 0
		for _, s := range sites {
			if !(s.file == "coordinator.go" && strings.HasSuffix(s.fn, "Coordinator.Reload")) {
				bad++
			}
		}
		if bad == 0 {
			t.Fatal("expected param-receiver Apply helper evasion to be detected")
		}
	})

	t.Run("rejects_local_state_alias_Snapshot_call", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
func newReloadState() *ReloadState { return &ReloadState{} }
func (s *ReloadState) Snapshot() {}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { state *ReloadState }
func NewCoordinator() *Coordinator { return &Coordinator{state: newReloadState()} }
func (c *Coordinator) Status() { c.state.Snapshot() }
func (c *Coordinator) sneak() {
	s := c.state
	s.Snapshot()
}
`,
		})
		sites, _ := findReloadStateMethodSites(files, "Snapshot")
		bad := 0
		for _, s := range sites {
			if !(s.file == "coordinator.go" && strings.HasSuffix(s.fn, "Coordinator.Status")) {
				bad++
			}
		}
		if bad == 0 {
			t.Fatal("expected local state alias Snapshot call to be detected")
		}
	})

	t.Run("rejects_method_value_alias_of_Apply", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
func newReloadState() *ReloadState { return &ReloadState{} }
func (s *ReloadState) Apply() {}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { state *ReloadState }
func NewCoordinator() *Coordinator { return &Coordinator{state: newReloadState()} }
func (c *Coordinator) Reload() {
	apply := c.state.Apply
	apply()
}
`,
		})
		_, methodVals := findReloadStateMethodSites(files, "Apply")
		if !violationContains(methodVals, "method-value") {
			t.Fatalf("expected method-value Apply alias rejection; got %v", methodVals)
		}
	})

	t.Run("unrelated_Apply_method_on_other_type_not_counted", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
func newReloadState() *ReloadState { return &ReloadState{} }
func (s *ReloadState) Apply() {}
`,
			"coordinator.go": `
package runtimehost
type Coordinator struct { state *ReloadState }
func NewCoordinator() *Coordinator { return &Coordinator{state: newReloadState()} }
func (c *Coordinator) Reload() { c.state.Apply(); c.state.Apply() }
`,
			"decoy.go": `
package runtimehost
type decoyState struct{}
func (d *decoyState) Apply() {}
func useDecoy(d *decoyState) { d.Apply() }
`,
		})
		sites, _ := findReloadStateMethodSites(files, "Apply")
		for _, s := range sites {
			if s.file == "decoy.go" {
				t.Fatalf("unrelated decoyState.Apply must not be counted: %+v", s)
			}
		}
	})

	t.Run("rejects_renamed_complete_shape_on_Coordinator", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
`,
			"coordinator.go": `
package runtimehost
import "sync"
type Coordinator struct {
	lock sync.Mutex
	eff *config.EffectiveConfig
	src *configsource.ActiveSourceVersion
	a sdkreload.Result
	b sdkreload.Result
	c sdkreload.Result
	posture string
	model string
	ring []sdkreload.HistoryEntry
	cap int
}
`,
		})
		got := analyzeReloadStateCompleteShape(files)
		if !violationContains(got, "Coordinator") {
			t.Fatalf("expected renamed complete shape on Coordinator rejection; got %v", got)
		}
	})

	t.Run("rejects_renamed_equivalent_owner_elsewhere", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
`,
			"shadow.go": `
package runtimehost
import "sync"
type shadowBag struct {
	mu sync.Mutex
	activeEff *config.EffectiveConfig
	activeSource *configsource.ActiveSourceVersion
	last sdkreload.Result
	ok sdkreload.Result
	fail sdkreload.Result
	srcPosture string
	modelGen string
	history []sdkreload.HistoryEntry
	historyCap int
}
`,
		})
		got := analyzeReloadStateCompleteShape(files)
		if !violationContains(got, "shadowBag") {
			t.Fatalf("expected renamed equivalent owner rejection; got %v", got)
		}
	})

	t.Run("rejects_aliased_types_hiding_complete_shape", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
`,
			"alias.go": `
package runtimehost
import "sync"
type EffPtr = *config.EffectiveConfig
type SrcPtr = *configsource.ActiveSourceVersion
type Res = sdkreload.Result
type Hist = []sdkreload.HistoryEntry
type hiddenOwner struct {
	mu sync.Mutex
	e EffPtr
	s SrcPtr
	r1 Res
	r2 Res
	r3 Res
	p1 string
	p2 string
	h Hist
	n int
}
`,
		})
		got := analyzeReloadStateCompleteShape(files)
		if !violationContains(got, "hiddenOwner") {
			t.Fatalf("expected aliased complete-shape rejection; got %v", got)
		}
	})

	t.Run("accepts_unrelated_partial_structs", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
`,
			"partial.go": `
package runtimehost
import "sync"
type onlyMutex struct{ mu sync.Mutex }
type onlyResult struct{ last sdkreload.Result }
type historyOnly struct{ history []sdkreload.HistoryEntry; cap int }
type sourceOnly struct{ src *configsource.ActiveSourceVersion }
`,
		})
		got := analyzeReloadStateCompleteShape(files)
		if violationContains(got, "onlyMutex") || violationContains(got, "onlyResult") ||
			violationContains(got, "historyOnly") || violationContains(got, "sourceOnly") {
			t.Fatalf("partial structs must remain accepted; got %v", got)
		}
	})

	t.Run("rejects_HistoryEntry_slice_on_observer", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"observability.go": `
package runtimehost
type ReloadObserver struct { events []sdkreload.HistoryEntry }
func (o *ReloadObserver) record(e sdkreload.HistoryEntry) { o.events = append(o.events, e) }
`,
		})
		got := scanObserverForHistoryOwnership(files)
		if !violationContains(got, "HistoryEntry") && !violationContains(got, "append") {
			t.Fatalf("expected HistoryEntry slice/append rejection; got %v", got)
		}
	})

	t.Run("rejects_StatusHistory_alias_on_observer", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"observability.go": `
package runtimehost
type HistBag = configreload.StatusHistory
type ReloadObserver struct { bag *HistBag }
func (o *ReloadObserver) Events() *HistBag { return o.bag }
`,
		})
		got := scanObserverForHistoryOwnership(files)
		if !violationContains(got, "history") && !violationContains(got, "StatusHistory") && !violationContains(got, "HistBag") {
			t.Fatalf("expected StatusHistory alias rejection; got %v", got)
		}
	})

	t.Run("accepts_observer_unrelated_telemetry_slice", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"observability.go": `
package runtimehost
type telemetryEvent struct{ msg string }
type ReloadObserver struct{ log int; events []telemetryEvent }
type ReloadObserverDeps struct{ Logger int }
func (o *ReloadObserver) Log(msg string) { o.events = append(o.events, telemetryEvent{msg: msg}) }
`,
		})
		got := scanObserverForHistoryOwnership(files)
		if len(got) > 0 {
			t.Fatalf("unrelated telemetry slices/log methods must be accepted; got %v", got)
		}
	})

	t.Run("rejects_reload_state_storing_Manager_under_alias", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type MgrAlias = *Manager
type ReloadState struct { mgr MgrAlias }
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if !violationContains(got, "Manager") {
			t.Fatalf("expected Manager-under-alias rejection; got %v", got)
		}
	})

	t.Run("rejects_reload_state_function_hook_fields", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct {
	onApply func()
}
type reloadStateInitial struct {
	hook func(int) error
}
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if !violationContains(got, "callback") && !violationContains(got, "hook") {
			t.Fatalf("expected function-typed hook field rejection; got %v", got)
		}
	})

	t.Run("rejects_generic_hook_registry_type", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
type reloadHookRegistry struct{}
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if !violationContains(got, "hook") {
			t.Fatalf("expected hook registry type rejection; got %v", got)
		}
	})

	t.Run("accepts_reload_state_with_only_allowed_value_deps", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
import (
	"sync"
	"time"
)
type ReloadState struct {
	mu sync.Mutex
	ts time.Time
	n int
	s string
}
type reloadStatusInput struct {
	ActiveGeneration int64
	Busy bool
}
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if len(got) > 0 {
			t.Fatalf("allowed value dependencies must not be flagged; got %v", got)
		}
	})

	t.Run("rejects_reload_state_importing_slog", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
import "log/slog"
var _ = slog.Default
type ReloadState struct{}
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if !violationContains(got, "log/slog") {
			t.Fatalf("expected log/slog import rejection; got %v", got)
		}
	})

	// --- transitive complete-shape / nested collaborator / observer-history fixtures ---

	t.Run("rejects_shadowState_composed_from_activePart_and_terminalPart", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
`,
			"shadow.go": `
package runtimehost
import "sync"
type activePart struct {
	eff *config.EffectiveConfig
	src *configsource.ActiveSourceVersion
}
type terminalPart struct {
	mu sync.Mutex
	r1, r2, r3 sdkreload.Result
	p1, p2 string
	events []sdkreload.HistoryEntry
	limit int
}
type shadowState struct {
	active activePart
	terminal terminalPart
}
`,
		})
		got := analyzeReloadStateCompleteShape(files)
		if !violationContains(got, "shadowState") {
			t.Fatalf("expected shadowState split-storage rejection; got %v", got)
		}
		if violationContains(got, "activePart") || violationContains(got, "terminalPart") {
			t.Fatalf("partial part structs must remain accepted; got %v", got)
		}
	})

	t.Run("rejects_Coordinator_composed_from_split_parts", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
`,
			"coordinator.go": `
package runtimehost
import "sync"
type activePart struct {
	eff *config.EffectiveConfig
	src *configsource.ActiveSourceVersion
}
type terminalPart struct {
	mu sync.Mutex
	r1, r2, r3 sdkreload.Result
	p1, p2 string
	events []sdkreload.HistoryEntry
	limit int
}
type Coordinator struct {
	active activePart
	terminal terminalPart
}
`,
		})
		got := analyzeReloadStateCompleteShape(files)
		if !violationContains(got, "Coordinator") {
			t.Fatalf("expected Coordinator split-parts rejection; got %v", got)
		}
	})

	t.Run("rejects_split_graph_with_aliases_pointers_and_embedding", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
`,
			"embed.go": `
package runtimehost
import "sync"
type activePart struct {
	eff *config.EffectiveConfig
	src *configsource.ActiveSourceVersion
}
type terminalPart struct {
	mu sync.Mutex
	r1, r2, r3 sdkreload.Result
	p1, p2 string
	events []sdkreload.HistoryEntry
	limit int
}
type ActiveAlias = activePart
type TermPtr = *terminalPart
type embeddedOwner struct {
	ActiveAlias
	TermPtr
}
`,
		})
		got := analyzeReloadStateCompleteShape(files)
		if !violationContains(got, "embeddedOwner") {
			t.Fatalf("expected alias/pointer/embedding split-graph rejection; got %v", got)
		}
	})

	t.Run("accepts_cyclic_partial_struct_references", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
`,
			"cycle.go": `
package runtimehost
import "sync"
type cyclicPartial struct {
	mu sync.Mutex
	next *cyclicPartial
	peer *otherPartial
}
type otherPartial struct {
	back *cyclicPartial
	n int
}
`,
		})
		got := analyzeReloadStateCompleteShape(files)
		if violationContains(got, "cyclicPartial") || violationContains(got, "otherPartial") {
			t.Fatalf("cyclic partial structs must remain accepted; got %v", got)
		}
	})

	t.Run("accepts_unrelated_owner_with_only_one_partial_child", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{}
`,
			"partial_owner.go": `
package runtimehost
type activePart struct {
	eff *config.EffectiveConfig
	src *configsource.ActiveSourceVersion
}
type onlyActiveHolder struct {
	part activePart
}
`,
		})
		got := analyzeReloadStateCompleteShape(files)
		if violationContains(got, "onlyActiveHolder") || violationContains(got, "activePart") {
			t.Fatalf("unrelated single-partial owner must remain accepted; got %v", got)
		}
	})

	t.Run("rejects_nested_Manager_box_on_ReloadState", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type Manager struct{}
type managerBox struct { mgr *Manager }
type ReloadState struct { box managerBox }
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if !violationContains(got, "Manager") {
			t.Fatalf("expected nested Manager box rejection; got %v", got)
		}
	})

	t.Run("rejects_function_type_alias_field_on_ReloadState", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type StateHook func()
type ReloadState struct { callback StateHook }
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if !violationContains(got, "callback") && !violationContains(got, "hook") && !violationContains(got, "StateHook") {
			t.Fatalf("expected function-type alias field rejection; got %v", got)
		}
	})

	t.Run("rejects_ReloadState_method_Manager_param_and_Publish", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type Manager struct{}
func (m *Manager) Publish(v any) {}
type ReloadState struct{}
func (s *ReloadState) bad(m *Manager) { m.Publish(nil) }
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if !violationContains(got, "Manager") && !violationContains(got, "Publish") {
			t.Fatalf("expected Manager param/Publish rejection; got %v", got)
		}
	})

	t.Run("rejects_ReloadState_method_callback_param_and_local_invocation", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type StateHook func()
type ReloadState struct{}
func (s *ReloadState) bad(cb StateHook) {
	local := cb
	local()
}
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if !violationContains(got, "callback") && !violationContains(got, "StateHook") && !violationContains(got, "invoke") {
			t.Fatalf("expected callback param/local invocation rejection; got %v", got)
		}
	})

	t.Run("rejects_nested_hook_registry_owned_by_ReloadState", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type nestedHookRegistry struct{}
type ReloadState struct { hooks nestedHookRegistry }
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if !violationContains(got, "hook") {
			t.Fatalf("expected nested hook registry rejection; got %v", got)
		}
	})

	t.Run("accepts_unrelated_hook_registry_in_other_production_file", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{ n int }
`,
			"lifecycle.go": `
package runtimehost
type lifecycleHookRegistry struct{ n int }
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if violationContains(got, "lifecycleHookRegistry") {
			t.Fatalf("unrelated hook registry outside ReloadState ownership must be accepted; got %v", got)
		}
	})

	t.Run("accepts_unrelated_callback_telemetry_not_referenced_by_ReloadState", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ReloadState struct{ n int }
`,
			"telemetry.go": `
package runtimehost
type TelemetryHook func(string)
type telemetrySink struct{ onEmit TelemetryHook }
func (t *telemetrySink) Emit(msg string) { t.onEmit(msg) }
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if violationContains(got, "telemetrySink") || violationContains(got, "TelemetryHook") {
			t.Fatalf("unrelated callback telemetry must be accepted; got %v", got)
		}
	})

	t.Run("rejects_observer_nested_history_box", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"observability.go": `
package runtimehost
type historyBox struct { events []sdkreload.HistoryEntry }
type ReloadObserver struct { box historyBox }
`,
		})
		got := scanObserverForHistoryOwnership(files)
		if !violationContains(got, "HistoryEntry") && !violationContains(got, "history") {
			t.Fatalf("expected nested history box rejection; got %v", got)
		}
	})

	t.Run("rejects_observer_package_global_history_mutation", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"observability.go": `
package runtimehost
var observerEvents []sdkreload.HistoryEntry
type ReloadObserver struct{}
func (o *ReloadObserver) record(e sdkreload.HistoryEntry) {
	observerEvents = append(observerEvents, e)
}
`,
		})
		got := scanObserverForHistoryOwnership(files)
		if !violationContains(got, "observerEvents") && !violationContains(got, "append") && !violationContains(got, "global") {
			t.Fatalf("expected package-global history mutation rejection; got %v", got)
		}
	})

	t.Run("rejects_observer_local_alias_from_nested_field_and_global", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"observability.go": `
package runtimehost
var observerEvents []sdkreload.HistoryEntry
type historyBox struct { events []sdkreload.HistoryEntry }
type ReloadObserver struct { box historyBox }
func (o *ReloadObserver) sneakField(e sdkreload.HistoryEntry) {
	local := o.box.events
	local = append(local, e)
	_ = local
}
func (o *ReloadObserver) sneakGlobal(e sdkreload.HistoryEntry) {
	local := observerEvents
	local = append(local, e)
	_ = local
}
`,
		})
		got := scanObserverForHistoryOwnership(files)
		if !violationContains(got, "append") && !violationContains(got, "history") && !violationContains(got, "observerEvents") {
			t.Fatalf("expected local alias history mutation rejection; got %v", got)
		}
	})

	t.Run("rejects_observer_accessor_returning_nested_history", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"observability.go": `
package runtimehost
type historyBox struct { events []sdkreload.HistoryEntry }
type ReloadObserver struct { box historyBox }
func (o *ReloadObserver) Events() []sdkreload.HistoryEntry { return o.box.events }
`,
		})
		got := scanObserverForHistoryOwnership(files)
		if !violationContains(got, "HistoryEntry") && !violationContains(got, "Events") && !violationContains(got, "history") {
			t.Fatalf("expected nested history accessor rejection; got %v", got)
		}
	})

	t.Run("accepts_observer_unrelated_telemetry_globals_and_nested_boxes", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"observability.go": `
package runtimehost
var telemetryLog []string
type stringBox struct { msgs []string }
type ReloadObserver struct { box stringBox }
func (o *ReloadObserver) Log(msg string) {
	o.box.msgs = append(o.box.msgs, msg)
	telemetryLog = append(telemetryLog, msg)
}
`,
		})
		got := scanObserverForHistoryOwnership(files)
		if len(got) > 0 {
			t.Fatalf("unrelated telemetry collections/globals must be accepted; got %v", got)
		}
	})

	// --- Defect 1: transitive package-local helper provenance from ReloadState ---

	t.Run("rejects_one_hop_helper_using_Manager_global", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type Manager struct{}
func (m *Manager) Publish(v any) {}
var hiddenManager *Manager
func publishFromState() { hiddenManager.Publish(nil) }
type ReloadState struct{}
func (s *ReloadState) Apply() { publishFromState() }
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if !violationContains(got, "Manager") && !violationContains(got, "Publish") && !violationContains(got, "hiddenManager") {
			t.Fatalf("expected one-hop Manager-global helper rejection; got %v", got)
		}
	})

	t.Run("rejects_two_hop_helper_chain_to_Manager", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type Manager struct{}
func (m *Manager) Publish(v any) {}
var hiddenManager *Manager
func publishFromState() { hiddenManager.Publish(nil) }
func helper2() { publishFromState() }
type ReloadState struct{}
func (s *ReloadState) Apply() { helper2() }
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if !violationContains(got, "Manager") && !violationContains(got, "Publish") && !violationContains(got, "helper") {
			t.Fatalf("expected two-hop helper-chain rejection; got %v", got)
		}
	})

	t.Run("rejects_helper_calling_renamed_factory_returning_Manager", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type Manager struct{}
func (m *Manager) Publish(v any) {}
func NewManager() (*Manager, error) { return &Manager{}, nil }
func buildMgr() (*Manager, error) { return NewManager() }
func newManagerFromState() {
	m, _ := buildMgr()
	m.Publish(nil)
}
type ReloadState struct{}
func (s *ReloadState) Apply() { newManagerFromState() }
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if !violationContains(got, "Manager") && !violationContains(got, "Publish") && !violationContains(got, "buildMgr") {
			t.Fatalf("expected renamed factory returning *Manager rejection; got %v", got)
		}
	})

	t.Run("rejects_callable_alias_to_forbidden_helper", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type Manager struct{}
func (m *Manager) Publish(v any) {}
var hiddenManager *Manager
func publishFromState() { hiddenManager.Publish(nil) }
type ReloadState struct{}
func (s *ReloadState) Apply() {
	alias := publishFromState
	alias()
}
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if !violationContains(got, "Manager") && !violationContains(got, "Publish") && !violationContains(got, "alias") {
			t.Fatalf("expected callable-alias helper rejection; got %v", got)
		}
	})

	t.Run("rejects_reachable_helper_callback_param_and_invocation", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type StateHook func()
func runHook(cb StateHook) { cb() }
type ReloadState struct{}
func (s *ReloadState) Apply() { runHook(func() {}) }
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if !violationContains(got, "callback") && !violationContains(got, "StateHook") && !violationContains(got, "invoke") {
			t.Fatalf("expected reachable helper callback rejection; got %v", got)
		}
	})

	t.Run("accepts_cyclic_safe_helper_chain_from_ReloadState", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
func ping(n int) int {
	if n <= 0 {
		return 0
	}
	return pong(n - 1)
}
func pong(n int) int { return ping(n) }
func normalizeCap(n int) int {
	if n <= 0 {
		return 32
	}
	return n
}
type ReloadState struct{}
func (s *ReloadState) Apply() {
	_ = ping(2)
	_ = normalizeCap(4)
}
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if len(got) > 0 {
			t.Fatalf("cyclic/safe value helpers must be accepted; got %v", got)
		}
	})

	t.Run("accepts_unrelated_forbidden_helper_not_reachable_from_ReloadState", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type Manager struct{}
func (m *Manager) Publish(v any) {}
var hiddenManager *Manager
func evilPublish() { hiddenManager.Publish(nil) }
type ReloadState struct{ n int }
func (s *ReloadState) Apply() { _ = s.n }
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if violationContains(got, "evilPublish") || violationContains(got, "hiddenManager") || violationContains(got, "Manager") {
			t.Fatalf("unrelated unreachable forbidden helper must be accepted; got %v", got)
		}
	})

	t.Run("accepts_ordinary_safe_value_helper_called_by_ReloadState", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"reload_state.go": `
package runtimehost
type ActiveSourceVersion struct{ Path string }
func cloneActiveSource(in *ActiveSourceVersion) *ActiveSourceVersion {
	if in == nil {
		return nil
	}
	cp := *in
	return &cp
}
func sanitizeHistoryActor(s string) string {
	if len(s) > 64 {
		return s[:64]
	}
	return s
}
type ReloadState struct{ src *ActiveSourceVersion }
func (s *ReloadState) Apply() {
	s.src = cloneActiveSource(s.src)
	_ = sanitizeHistoryActor("actor")
}
`,
		})
		got := scanReloadStateForbiddenDependencies(files)
		if len(got) > 0 {
			t.Fatalf("ordinary safe value helpers must be accepted; got %v", got)
		}
	})

	// --- Defect 2: package-global canonical history inference / permanent rejection ---

	t.Run("rejects_package_global_history_via_make", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"observability.go": `
package runtimehost
var observerEvents = make([]sdkreload.HistoryEntry, 0, 32)
type ReloadObserver struct{}
`,
		})
		got := scanObserverForHistoryOwnership(files)
		if !violationContains(got, "observerEvents") && !violationContains(got, "HistoryEntry") && !violationContains(got, "global") {
			t.Fatalf("expected make()-inferred package-global history rejection; got %v", got)
		}
	})

	t.Run("rejects_package_global_StatusHistory_via_new_and_constructor", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"observability.go": `
package runtimehost
type StatusHistory struct{}
func NewStatusHistory() *StatusHistory { return &StatusHistory{} }
var histPtr = new(StatusHistory)
var histCtor = NewStatusHistory()
type ReloadObserver struct{}
`,
		})
		got := scanObserverForHistoryOwnership(files)
		if !violationContains(got, "histPtr") && !violationContains(got, "histCtor") && !violationContains(got, "StatusHistory") {
			t.Fatalf("expected new/constructor StatusHistory global rejection; got %v", got)
		}
	})

	t.Run("rejects_package_global_history_alias_chain", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"observability.go": `
package runtimehost
var observerEvents []sdkreload.HistoryEntry
var aliasEvents = observerEvents
var aliasEvents2 = aliasEvents
type ReloadObserver struct{}
`,
		})
		got := scanObserverForHistoryOwnership(files)
		if !violationContains(got, "aliasEvents") && !violationContains(got, "observerEvents") && !violationContains(got, "HistoryEntry") {
			t.Fatalf("expected global history alias-chain rejection; got %v", got)
		}
	})

	t.Run("rejects_observer_helper_indirection_mutating_global_history", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"observability.go": `
package runtimehost
var observerEvents = make([]sdkreload.HistoryEntry, 0, 32)
func appendObserverEvent(e sdkreload.HistoryEntry) {
	observerEvents = append(observerEvents, e)
}
type ReloadObserver struct{}
func (o *ReloadObserver) record(e sdkreload.HistoryEntry) {
	appendObserverEvent(e)
}
`,
		})
		got := scanObserverForHistoryOwnership(files)
		if !violationContains(got, "observerEvents") && !violationContains(got, "HistoryEntry") && !violationContains(got, "global") {
			t.Fatalf("expected Observer→helper global history rejection; got %v", got)
		}
	})

	t.Run("accepts_unrelated_telemetry_globals_and_local_ephemeral_history", func(t *testing.T) {
		t.Parallel()
		files := mustParseSyntheticFiles(t, map[string]string{
			"observability.go": `
package runtimehost
var telemetryLog []string
var telemetryCounts = map[string]int{}
type ReloadObserver struct{}
func (o *ReloadObserver) ephemeral(e sdkreload.HistoryEntry) {
	local := []sdkreload.HistoryEntry{}
	local = append(local, e)
	_ = local
}
`,
		})
		got := scanObserverForHistoryOwnership(files)
		if len(got) > 0 {
			t.Fatalf("unrelated telemetry globals and local ephemeral history must be accepted; got %v", got)
		}
	})
}
