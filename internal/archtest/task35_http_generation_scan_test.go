package archtest

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
)

// Task 3.5 gate identifiers. Integrated into the shared convergence allowlist
// for Phase 4 Built grandfather sites (stdhttp_built) and candidate legacy
// closer projection sites (candidate_legacy_closers). All other Task 3.5 gates
// are zero-tolerance (no allowlist entries).
const (
	gateBroadRequestPlane        = "broad_request_plane"
	gateCompatHTTPSymbols        = "compat_http_symbols"
	gateFocusedHTTPLifecycle     = "focused_http_lifecycle"
	gateStdhttpBuilt             = "stdhttp_built"
	gateCanonicalClosers         = "canonical_generation_closers"
	gateCandidateLegacyClosers   = "candidate_legacy_closers"
	gateComposeInventory         = "compose_inventory"
)

var task35ForbiddenCompatSymbols = map[string]bool{
	"NewCompatRequestPlane":             true,
	"ComposeRequestPlane":               true,
	"standardHTTPInputFromRequestPlane": true,
	"requestPlaneAsBuilt":               true,
}

// focusedHTTPLifecycleSurfaces are production symbols that must remain
// lifecycle-free and free of generic dependency bags (req 3.7, 9.1).
var focusedHTTPLifecycleSurfaces = map[string]bool{
	"StandardHTTPInput":      true,
	"HTTPCoreInput":          true,
	"HTTPSecurityInput":      true,
	"HTTPOperationsInput":    true,
	"HTTPModelInput":         true,
	"HTTPFrontendInput":      true,
	"ComposeStandardHTTP":    true,
	"prepareStandardHandler": true,
	"mountMetrics":           true,
	"mountDiagnostics":       true,
	"mountModelCatalogDiagnostics":   true,
	"mountModelInventoryDiagnostics": true,
	"mountSecureSessionDiagnostics":  true,
	"mountAccountingAdmin":           true,
	"mountControlPlaneQuery":         true,
	"mountAccountingAuthorityQuery":  true,
	"MountBundledFrontends":          true,
	"MountBundledFrontendsLegacy":    true,
	"mountALegCancel":                true,
	"stackHTTPHandler":               true,
}

var focusedHTTPLifecycleCallNames = map[string]bool{
	"Close":    true,
	"Quiesce":  true,
	"Shutdown": true,
	"Start":    true,
	"Stop":     true,
}

var focusedHTTPLifecycleParamNames = map[string]bool{
	"Closers":        true,
	"Closer":         true,
	"Close":          true,
	"Shutdown":       true,
	"Quiesce":        true,
	"OnClose":        true,
	"OnShutdown":     true,
	"ReleaseClosers": true,
	"ResourceLedger": true,
	"Ledger":         true,
	"Host":           true,
	"Coordinator":    true,
}

// scanBroadRequestPlaneSource detects the deleted broad runtimebundle.RequestPlane
// aggregate / one-getter-per-dependency wall (req 3.4-3.5). runtimehost's narrow
// PublishedRequestPlane is intentionally out of scope.
func scanBroadRequestPlaneSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	if !isRuntimebundlePath(rel) {
		return nil, nil
	}
	var out []convergenceFinding
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil || ts.Name.Name != "RequestPlane" {
					continue
				}
				// Narrow publication aliases named RequestPlane are not in runtimebundle.
				if ts.Assign.IsValid() {
					continue
				}
				if _, ok := ts.Type.(*ast.StructType); !ok {
					continue
				}
				out = append(out, convergenceFinding{
					Gate: gateBroadRequestPlane, Path: rel, Identity: "type:RequestPlane",
					Classification: classDeclaration,
					Detail:         formatPos(fset, ts.Name.Pos()) + " broad runtimebundle.RequestPlane aggregate",
				})
			}
		case *ast.FuncDecl:
			if d.Name == nil {
				continue
			}
			if d.Name.Name == "NewCompatRequestPlane" {
				out = append(out, convergenceFinding{
					Gate: gateBroadRequestPlane, Path: rel, Identity: "func:NewCompatRequestPlane",
					Classification: classAdapter,
					Detail:         formatPos(fset, d.Name.Pos()) + " RequestPlane compatibility constructor",
				})
			}
			if d.Recv == nil || len(d.Recv.List) != 1 {
				continue
			}
			if !recvIsRequestPlaneValue(d.Recv.List[0].Type) {
				continue
			}
			out = append(out, convergenceFinding{
				Gate: gateBroadRequestPlane, Path: rel,
				Identity:       "method:RequestPlane." + d.Name.Name,
				Classification: classDeclaration,
				Detail:         formatPos(fset, d.Name.Pos()) + " RequestPlane getter-wall method",
			})
		}
	}
	return out, nil
}

func recvIsRequestPlaneValue(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "RequestPlane"
	case *ast.StarExpr:
		id, ok := t.X.(*ast.Ident)
		return ok && id.Name == "RequestPlane"
	default:
		return false
	}
}

// scanCompatHTTPSymbolsSource detects production reintroduction of deleted
// RequestPlane compatibility constructors/composers/projectors (req 3.4, 11.4).
func scanCompatHTTPSymbolsSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	aliases := importAliasToPath(f)
	var out []convergenceFinding

	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil {
			continue
		}
		if task35ForbiddenCompatSymbols[fd.Name.Name] {
			out = append(out, convergenceFinding{
				Gate: gateCompatHTTPSymbols, Path: rel, Identity: "func:" + fd.Name.Name,
				Classification: classAdapter,
				Detail:         formatPos(fset, fd.Name.Pos()) + " forbidden compatibility symbol",
			})
		}
	}

	ordinals := callSiteOrdinals{}
	localUnqualified := map[string]string{}
	for name := range task35ForbiddenCompatSymbols {
		localUnqualified[name] = name
	}
	prot := protectedSymbolSet{}
	for name := range task35ForbiddenCompatSymbols {
		prot[name] = true
		prot["stdhttp."+name] = true
		prot["runtimebundle."+name] = true
	}
	toShort := func(resolved string) (string, bool) {
		base := resolved
		if i := strings.LastIndex(resolved, "."); i >= 0 {
			base = resolved[i+1:]
		}
		if task35ForbiddenCompatSymbols[base] {
			return base, true
		}
		return "", false
	}
	visitor := &protectedCallVisitor{
		importAliases:    aliases,
		dotPaths:         dotImportedProtectedPaths(f, map[string]bool{importRuntimebundle: true, importStdhttp: true}),
		localUnqualified: localUnqualified,
		protected:        prot,
		toShort:          toShort,
		ordinals:         ordinals,
		onCall: func(identity string, call *ast.CallExpr, shortLabel string) {
			out = append(out, convergenceFinding{
				Gate: gateCompatHTTPSymbols, Path: rel,
				Identity:       identity,
				Classification: classCall,
				Detail:         formatPos(fset, call.Pos()) + " call " + shortLabel,
			})
		},
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		visitor.walkFunc(fd)
	}
	return out, nil
}

// scanFocusedHTTPLifecycleSource fails when focused HTTP composition surfaces
// gain closer/lifecycle ownership or generic dependency bags (req 3.7, 9.1).
func scanFocusedHTTPLifecycleSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	if !isStdhttpPath(rel) {
		return nil, nil
	}
	// Skip admin subpackages; mounts/composer live in root stdhttp (+ contract).
	if strings.Contains(rel, "/stdhttp/admin/") {
		return nil, nil
	}
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	aliases := importAliasToPath(f)
	localTypes := map[string]*ast.TypeSpec{}
	typeAliases := map[string]ast.Expr{}
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
			localTypes[ts.Name.Name] = ts
			if ts.Assign.IsValid() {
				typeAliases[ts.Name.Name] = ts.Type
			}
		}
	}

	var out []convergenceFinding
	for name, ts := range localTypes {
		if !focusedHTTPLifecycleSurfaces[name] {
			continue
		}
		st := resolveStructType(ts, typeAliases, localTypes)
		if st == nil {
			continue
		}
		for _, field := range st.Fields.List {
			for _, n := range field.Names {
				if focusedHTTPLifecycleParamNames[n.Name] || prohibitedLifecycleFieldNames[n.Name] {
					out = append(out, convergenceFinding{
						Gate: gateFocusedHTTPLifecycle, Path: rel,
						Identity: "field:" + name + "." + n.Name, Classification: classDeclaration,
						Detail: formatPos(fset, n.Pos()) + " lifecycle/broad field on focused HTTP surface",
					})
				}
				if isAnyOrEmptyInterface(field.Type) || isMapType(field.Type) {
					out = append(out, convergenceFinding{
						Gate: gateFocusedHTTPLifecycle, Path: rel,
						Identity: "field:" + name + "." + n.Name, Classification: classDeclaration,
						Detail: formatPos(fset, n.Pos()) + " generic dependency bag on focused HTTP surface",
					})
				}
			}
		}
	}

	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || !focusedHTTPLifecycleSurfaces[fd.Name.Name] {
			continue
		}
		if fd.Type != nil && fd.Type.Params != nil {
			for _, p := range fd.Type.Params.List {
				for _, n := range p.Names {
					if focusedHTTPLifecycleParamNames[n.Name] {
						out = append(out, convergenceFinding{
							Gate: gateFocusedHTTPLifecycle, Path: rel,
							Identity: "param:" + fd.Name.Name + "." + n.Name, Classification: classDeclaration,
							Detail: formatPos(fset, n.Pos()) + " lifecycle parameter on focused HTTP surface",
						})
					}
				}
				if typeLooksLikeBuiltOrRequestPlane(p.Type, aliases, typeAliases, localTypes, false) {
					// Built on NewStandardHandler is Phase 4; ComposeStandardHTTP/prepare must stay focused.
					if fd.Name.Name == "ComposeStandardHTTP" || fd.Name.Name == "prepareStandardHandler" {
						out = append(out, convergenceFinding{
							Gate: gateFocusedHTTPLifecycle, Path: rel,
							Identity: "param:" + fd.Name.Name + ".BuiltOrRequestPlane", Classification: classDeclaration,
							Detail: formatPos(fset, p.Type.Pos()) + " broad Built/RequestPlane on focused composer",
						})
					}
				}
				if isAnyOrEmptyInterface(p.Type) || isMapType(p.Type) {
					out = append(out, convergenceFinding{
						Gate: gateFocusedHTTPLifecycle, Path: rel,
						Identity: "param:" + fd.Name.Name + ".generic", Classification: classDeclaration,
						Detail: formatPos(fset, p.Type.Pos()) + " generic bag parameter on focused HTTP surface",
					})
				}
			}
		}
		if fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			ce, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := callSelectorName(ce.Fun)
			if focusedHTTPLifecycleCallNames[name] {
				// Ignore http.Server lifecycle and error methods named Close on unrelated values —
				// only flag bare/local Close/Quiesce/Shutdown/Start/Stop style ownership calls
				// when the selector is clearly a lifecycle verb on a non-ResponseWriter receiver.
				if name == "Close" || name == "Quiesce" || name == "Shutdown" || name == "Start" || name == "Stop" {
					out = append(out, convergenceFinding{
						Gate: gateFocusedHTTPLifecycle, Path: rel,
						Identity: "call:" + fd.Name.Name + "->" + name + "#lifecycle", Classification: classCall,
						Detail: formatPos(fset, ce.Pos()) + " lifecycle call inside focused HTTP composition",
					})
				}
			}
			return true
		})
	}
	return out, nil
}

func resolveStructType(ts *ast.TypeSpec, aliases map[string]ast.Expr, local map[string]*ast.TypeSpec) *ast.StructType {
	if ts == nil {
		return nil
	}
	cur := ts.Type
	for i := 0; i < 4; i++ {
		switch t := cur.(type) {
		case *ast.StructType:
			return t
		case *ast.Ident:
			if next, ok := aliases[t.Name]; ok {
				cur = next
				continue
			}
			if next, ok := local[t.Name]; ok {
				cur = next.Type
				continue
			}
			return nil
		case *ast.SelectorExpr:
			// External package type (contract.StandardHTTPInput) — field scan happens
			// in the defining package file; skip here.
			return nil
		default:
			return nil
		}
	}
	return nil
}

func callSelectorName(fun ast.Expr) string {
	switch t := fun.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		if t.Sel != nil {
			return t.Sel.Name
		}
	}
	return ""
}

// scanStdhttpBuiltSource detects any production root-stdhttp declaration whose
// AST contains a resolved runtimebundle.Built type reference (params, results,
// composite literals, conversions, type aliases/struct fields, vars, methods).
// Exact Phase 4 grandfather sites are allowlisted; anything else is new legacy
// growth (req 3.4, 12.5). File-wide ignores are intentionally not used.
func scanStdhttpBuiltSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	if !isStdhttpPath(rel) || strings.Contains(rel, "/stdhttp/admin/") || strings.Contains(rel, "/stdhttp/contract/") {
		return nil, nil
	}
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	aliases := importAliasToPath(f)
	dotRB := fileHasDotImport(f, importRuntimebundle)
	var out []convergenceFinding
	seen := map[string]bool{}
	emit := func(identity string, pos token.Pos) {
		if identity == "" || seen[identity] {
			return
		}
		seen[identity] = true
		out = append(out, convergenceFinding{
			Gate: gateStdhttpBuilt, Path: rel, Identity: identity,
			Classification: classDeclaration,
			Detail:         formatPos(fset, pos) + " stdhttp production declaration references runtimebundle.Built",
		})
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name == nil || !declContainsRuntimebundleBuilt(d, aliases, dotRB) {
				continue
			}
			emit(funcDeclStructuralIdentity(d), d.Name.Pos())
		case *ast.GenDecl:
			switch d.Tok {
			case token.TYPE:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name == nil {
						continue
					}
					if declContainsRuntimebundleBuilt(ts, aliases, dotRB) {
						emit("type:"+ts.Name.Name, ts.Name.Pos())
					}
				}
			case token.VAR, token.CONST:
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					if !declContainsRuntimebundleBuilt(vs, aliases, dotRB) {
						continue
					}
					if len(vs.Names) == 0 {
						emit("var:_", vs.Pos())
						continue
					}
					for _, n := range vs.Names {
						if n == nil {
							continue
						}
						prefix := "var:"
						if d.Tok == token.CONST {
							prefix = "const:"
						}
						emit(prefix+n.Name, n.Pos())
					}
				}
			}
		}
	}
	return out, nil
}

func fileHasDotImport(f *ast.File, path string) bool {
	for _, imp := range f.Imports {
		if imp.Name == nil || imp.Name.Name != "." {
			continue
		}
		if strings.Trim(imp.Path.Value, `"`) == path {
			return true
		}
	}
	return false
}

func funcDeclStructuralIdentity(fd *ast.FuncDecl) string {
	if fd == nil || fd.Name == nil {
		return ""
	}
	if fd.Recv != nil && len(fd.Recv.List) == 1 {
		return "method:" + recvTypeName(fd.Recv.List[0].Type) + "." + fd.Name.Name
	}
	return "func:" + fd.Name.Name
}

// declContainsRuntimebundleBuilt reports whether node mentions runtimebundle.Built
// as a type (including *Built), via selectors, dot-import Idents in type
// positions, composite literals, or conversions. Field/variable names named
// Built are ignored unless they appear as type expressions.
func declContainsRuntimebundleBuilt(n ast.Node, aliases map[string]string, dotRB bool) bool {
	if n == nil {
		return false
	}
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		if found || node == nil {
			return false
		}
		switch x := node.(type) {
		case *ast.Field:
			if exprIsRuntimebundleBuiltType(x.Type, aliases, dotRB) {
				found = true
				return false
			}
			// Continue into nested types (func params inside interfaces, etc.).
			return true
		case *ast.ValueSpec:
			if exprIsRuntimebundleBuiltType(x.Type, aliases, dotRB) {
				found = true
				return false
			}
			for _, v := range x.Values {
				if exprUsesRuntimebundleBuiltValue(v, aliases, dotRB) {
					found = true
					return false
				}
			}
			return false // children handled
		case *ast.TypeSpec:
			if exprIsRuntimebundleBuiltType(x.Type, aliases, dotRB) {
				found = true
				return false
			}
			return true
		case *ast.CompositeLit:
			if exprIsRuntimebundleBuiltType(x.Type, aliases, dotRB) {
				found = true
				return false
			}
			return true
		case *ast.CallExpr:
			// Conversions are CallExprs whose Fun is a type expression.
			if exprIsRuntimebundleBuiltType(x.Fun, aliases, dotRB) {
				found = true
				return false
			}
			return true
		case *ast.FuncType:
			if fieldListHasRuntimebundleBuilt(x.Params, aliases, dotRB) ||
				fieldListHasRuntimebundleBuilt(x.Results, aliases, dotRB) {
				found = true
				return false
			}
			return true
		}
		return true
	})
	return found
}

func fieldListHasRuntimebundleBuilt(fl *ast.FieldList, aliases map[string]string, dotRB bool) bool {
	if fl == nil {
		return false
	}
	for _, f := range fl.List {
		if exprIsRuntimebundleBuiltType(f.Type, aliases, dotRB) {
			return true
		}
	}
	return false
}

func exprUsesRuntimebundleBuiltValue(expr ast.Expr, aliases map[string]string, dotRB bool) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found || n == nil {
			return false
		}
		switch x := n.(type) {
		case *ast.CompositeLit:
			if exprIsRuntimebundleBuiltType(x.Type, aliases, dotRB) {
				found = true
				return false
			}
		case *ast.CallExpr:
			if exprIsRuntimebundleBuiltType(x.Fun, aliases, dotRB) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func exprIsRuntimebundleBuiltType(expr ast.Expr, aliases map[string]string, dotRB bool) bool {
	for expr != nil {
		switch t := expr.(type) {
		case *ast.ParenExpr:
			expr = t.X
		case *ast.StarExpr:
			expr = t.X
		case *ast.Ellipsis:
			expr = t.Elt
		case *ast.ArrayType:
			expr = t.Elt
		case *ast.Ident:
			return t.Name == "Built" && dotRB
		case *ast.SelectorExpr:
			return isRuntimebundleBuiltSelector(t, aliases)
		case *ast.MapType:
			return exprIsRuntimebundleBuiltType(t.Key, aliases, dotRB) ||
				exprIsRuntimebundleBuiltType(t.Value, aliases, dotRB)
		case *ast.ChanType:
			return exprIsRuntimebundleBuiltType(t.Value, aliases, dotRB)
		case *ast.FuncType:
			return fieldListHasRuntimebundleBuilt(t.Params, aliases, dotRB) ||
				fieldListHasRuntimebundleBuilt(t.Results, aliases, dotRB)
		case *ast.StructType:
			if t.Fields == nil {
				return false
			}
			for _, f := range t.Fields.List {
				if exprIsRuntimebundleBuiltType(f.Type, aliases, dotRB) {
					return true
				}
			}
			return false
		case *ast.InterfaceType:
			if t.Methods == nil {
				return false
			}
			for _, f := range t.Methods.List {
				if exprIsRuntimebundleBuiltType(f.Type, aliases, dotRB) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}

func isRuntimebundleBuiltSelector(sel *ast.SelectorExpr, aliases map[string]string) bool {
	if sel == nil || sel.Sel == nil || sel.Sel.Name != "Built" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	path := aliases[pkg.Name]
	return path == importRuntimebundle
}

// scanCandidateLegacyClosersSource detects CandidateRuntime's exported Closers
// field, selector use of .Closers, and ResourceLedger.LegacyClosers
// declaration/calls across all runtimebundle production files. Exact Phase 4
// grandfather sites are allowlisted; new production use fails (req 3.8, 12.5).
// Lower-case internals like quiesceClosers are intentionally ignored.
func scanCandidateLegacyClosersSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	if !isRuntimebundlePath(rel) {
		return nil, nil
	}
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	var out []convergenceFinding
	seen := map[string]bool{}
	emit := func(identity string, pos token.Pos) {
		if identity == "" || seen[identity] {
			return
		}
		seen[identity] = true
		out = append(out, convergenceFinding{
			Gate: gateCandidateLegacyClosers, Path: rel, Identity: identity,
			Classification: classDeclaration,
			Detail:         formatPos(fset, pos) + " candidate legacy Closers/LegacyClosers projection",
		})
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil || ts.Name.Name != "CandidateRuntime" {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				for _, field := range st.Fields.List {
					for _, n := range field.Names {
						if n != nil && n.Name == "Closers" {
							emit("type:CandidateRuntime", n.Pos())
						}
					}
				}
			}
		case *ast.FuncDecl:
			if d.Name == nil {
				continue
			}
			id := funcDeclStructuralIdentity(d)
			// Declaration of the legacy projection method itself.
			if d.Name.Name == "LegacyClosers" && d.Recv != nil && len(d.Recv.List) == 1 &&
				recvTypeName(d.Recv.List[0].Type) == "ResourceLedger" {
				emit(id, d.Name.Pos())
				continue
			}
			if d.Body == nil {
				continue
			}
			if funcUsesCandidateLegacyClosers(d.Body) {
				emit(id, d.Name.Pos())
			}
		}
	}
	return out, nil
}

func funcUsesCandidateLegacyClosers(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found || n == nil {
			return false
		}
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return true
		}
		switch sel.Sel.Name {
		case "Closers", "LegacyClosers":
			found = true
			return false
		}
		return true
	})
	return found
}

// scanCanonicalGenerationClosersSource detects closer-projection use on the
// canonical CompileGeneration / GenerationCompiler path (req 3.8). Candidate
// Closers / LegacyClosers remain Phase 4 declarations elsewhere.
func scanCanonicalGenerationClosersSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	base := filepath.Base(rel)
	switch base {
	case "compile_generation.go", "reload_host.go":
	default:
		return nil, nil
	}
	if !isRuntimebundlePath(rel) {
		return nil, nil
	}
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	var out []convergenceFinding
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Name == nil {
			continue
		}
		// Limit to CompileGeneration and GenerationCompiler.Compile.
		recvOK := false
		if fd.Name.Name == "Compile" && fd.Recv != nil && len(fd.Recv.List) == 1 {
			recvOK = recvTypeName(fd.Recv.List[0].Type) == "GenerationCompiler"
		}
		switch {
		case fd.Name.Name == "CompileGeneration":
		case recvOK:
		case fd.Name.Name == "buildStandardHTTPInput":
		case fd.Name.Name == "composeStandardHTTPIsolated":
		default:
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SelectorExpr:
				if node.Sel == nil {
					return true
				}
				switch node.Sel.Name {
				case "Closers", "LegacyClosers":
					out = append(out, convergenceFinding{
						Gate: gateCanonicalClosers, Path: rel,
						Identity:       "sel:" + fd.Name.Name + "->" + node.Sel.Name,
						Classification: classCall,
						Detail:         formatPos(fset, node.Pos()) + " canonical generation path references closer projection",
					})
				}
			case *ast.Ident:
				if node.Name == "RequestPlane" || node.Name == "Built" {
					out = append(out, convergenceFinding{
						Gate: gateCanonicalClosers, Path: rel,
						Identity:       "ident:" + fd.Name.Name + "->" + node.Name,
						Classification: classCall,
						Detail:         formatPos(fset, node.Pos()) + " canonical generation path references legacy aggregate",
					})
				}
			}
			return true
		})
	}
	return out, nil
}
