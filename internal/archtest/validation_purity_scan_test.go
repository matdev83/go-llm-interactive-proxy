package archtest

import (
	"go/ast"
	"go/token"
	"strconv"
)

// gateValidationPurity guards Task 5.4's ValidateDistribution invariant:
// runCheckConfigCommand and the focused runtimebundle validate_distribution.go
// operation graph must never reach a publication/host owner (Manager,
// generation publish, AttachReloadHost, BuildHost, BuildBootstrap, stdhttp
// server runners, or a listener), whether directly, via a local/package-scope
// alias, or via an obvious one-hop renamed wrapper. Unlike gateInspectPurity,
// this gate ALLOWS NewProcessServices and CompileGeneration: validation must
// use the same process/compile path as serve/reload, just never publish it.
const gateValidationPurity = "validation_purity"

// pathValidateOps is the one focused production file ValidateDistribution and
// its private helpers must live in (scan constants; mirrors pathInspectOps).
const pathValidateOps = "internal/infra/runtimebundle/validate_distribution.go"

// validationPurityRoleFuncNames are ValidateDistribution driving adapters and
// composition-root operations. Every function declared in pathValidateOps is
// also in-scope regardless of name (see validationPurityRoleFunc).
var validationPurityRoleFuncNames = map[string]bool{
	"runCheckConfigCommand": true,
	"ValidateDistribution":  true,
	"validateDistribution":  true,
}

// validationPurityProtected are publication/host-owning symbols validation
// must not reach. Includes same-package helpers (publishInitialGeneration,
// bindReloadHost) and scheduled/compatibility constructors that would violate
// the unpublished dry-run invariant if reached from a validation-role body.
var validationPurityProtected = protectedSymbolSet{
	"runtimebundle.BuildBootstrap":           true,
	"runtimebundle.BuildHost":                true,
	"runtimebundle.AttachReloadHost":         true,
	"runtimebundle.Build":                    true,
	"runtimebundle.NewBootstrapApp":          true,
	"runtimebundle.publishInitialGeneration": true,
	"runtimebundle.bindReloadHost":           true,
	"runtimebundle.NewReloadHost":            true,
	"runtimebundle.NewReloadCoordinator":     true,
	"runtimehost.NewManager":                 true,
	"runtimehost.NewManagerWithInstanceID":   true,
	"runtimehost.NewGeneration":              true,
	"runtimehost.NewCoordinator":             true,
	"runtimehost.NewReloadCoordinator":       true,
	"stdhttp.RunWithRuntime":                 true,
	"stdhttp.RunWithGenerationHost":          true,
	"net.Listen":                             true,
	"net/http.ListenAndServe":                true,
	"net/http.ListenAndServeTLS":             true,
}

var validationPurityDotPaths = map[string]bool{
	importRuntimebundle: true,
	importRuntimehost:   true,
	importStdhttp:       true,
	importNet:           true,
	importNetHTTP:       true,
}

// validationPurityForbiddenImports are ownership packages the focused
// validate_distribution.go must not import. runtimehost/tracing/net stay
// allowed at the package graph level elsewhere; this file's own scope is
// narrower than gateInspectPurity's because ValidateDistribution legitimately
// needs ProcessServices/CompileGeneration, which live in this same package.
var validationPurityForbiddenImports = map[string]string{
	importRuntimehost: "runtimehost",
	importStdhttp:     "stdhttp",
}

// validationPurityForbiddenMethodNames are publication/listener-owning method
// names detected structurally by selector name alone (receiver-independent),
// including local method-value aliases (publish := mgr.Publish; publish(...)).
// Scoped only to validation-role bodies / pathValidateOps so unrelated
// repository Serve/Listen methods outside this gate are never scanned.
var validationPurityForbiddenMethodNames = map[string]bool{
	"Publish":                  true,
	"PrepareRequestPlane":      true,
	"BeginPrepareRequestPlane": true,
	"Listen":                   true,
	"ListenAndServe":           true,
	"ListenAndServeTLS":        true,
	"Serve":                    true,
	"ServeTLS":                 true,
}

func scanValidationPuritySource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	aliases := importAliasToPath(f)
	var out []convergenceFinding

	if rel == pathValidateOps {
		out = append(out, scanValidationPurityForbiddenImports(rel, fset, f)...)
	}

	localUnqualified := validationPurityLocalUnqualified(rel)
	dotPaths := dotImportedProtectedPaths(f, validationPurityDotPaths)
	pkgScope := packageScopeProtectedAliases(f, aliases, dotPaths, localUnqualified, validationPurityProtected)
	funcs := samePackageFuncDecls(f)
	wrappers := validationPurityWrapperDelegates(funcs, aliases, dotPaths, localUnqualified, pkgScope)

	ordinals := callSiteOrdinals{}
	toShort := func(resolved string) (string, bool) {
		if validationPurityProtected[resolved] {
			return resolved, true
		}
		return "", false
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Name == nil {
			continue
		}
		if !validationPurityRoleFunc(rel, fd.Name.Name) {
			continue
		}
		visitor := &protectedCallVisitor{
			importAliases:    aliases,
			dotPaths:         dotPaths,
			localUnqualified: localUnqualified,
			protected:        validationPurityProtected,
			toShort:          toShort,
			ordinals:         ordinals,
			onCall: func(identity string, call *ast.CallExpr, shortLabel string) {
				out = append(out, convergenceFinding{
					Gate: gateValidationPurity, Path: rel,
					Identity:       identity,
					Classification: classCall,
					Detail:         formatPos(fset, call.Pos()) + " forbidden publication/host call " + shortLabel + " from " + fd.Name.Name,
				})
			},
		}
		visitor.walkFuncWithPackageScope(fd, pkgScope)
		out = append(out, scanValidationPurityWrapperCalls(rel, fset, fd, wrappers, ordinals)...)
		out = append(out, scanValidationPurityForbiddenMethodCalls(rel, fset, fd, ordinals)...)
	}
	return out, nil
}

// validationPurityRoleFunc reports whether fd is in-scope for this gate:
// named role functions anywhere, plus every function declared in the one
// focused validate_distribution.go file (mirrors inspectPurityRoleFunc).
func validationPurityRoleFunc(rel, name string) bool {
	if validationPurityRoleFuncNames[name] {
		return true
	}
	return rel == pathValidateOps
}

func validationPurityLocalUnqualified(rel string) map[string]string {
	out := map[string]string{}
	if !isRuntimebundlePath(rel) {
		return out
	}
	for label := range validationPurityProtected {
		if s, ok := trimPrefixed(label, "runtimebundle."); ok {
			out[s] = label
		}
	}
	return out
}

func trimPrefixed(s, prefix string) (string, bool) {
	if len(s) <= len(prefix) || s[:len(prefix)] != prefix {
		return "", false
	}
	return s[len(prefix):], true
}

func scanValidationPurityForbiddenImports(rel string, fset *token.FileSet, f *ast.File) []convergenceFinding {
	var out []convergenceFinding
	if f == nil {
		return out
	}
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		ipath := trimQuotes(imp.Path.Value)
		short, ok := validationPurityForbiddenImports[ipath]
		if !ok {
			continue
		}
		out = append(out, convergenceFinding{
			Gate: gateValidationPurity, Path: rel,
			Identity:       "import:" + short,
			Classification: classDeclaration,
			Detail:         formatPos(fset, imp.Path.Pos()) + " validate_distribution.go must not import ownership package " + ipath,
		})
	}
	return out
}

func trimQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// validationPurityWrapperDelegates maps same-package function names whose
// bodies reach a forbidden owner (protected symbol or forbidden method /
// method-value alias) to that owner's short label (one-hop, obvious wrappers).
func validationPurityWrapperDelegates(
	funcs map[string]*ast.FuncDecl,
	aliases map[string]string,
	dotPaths []string,
	localUnqualified map[string]string,
	pkgScope *aliasScope,
) map[string]string {
	out := map[string]string{}
	toShort := func(resolved string) (string, bool) {
		if validationPurityProtected[resolved] {
			return resolved, true
		}
		return "", false
	}
	for name, fd := range funcs {
		if validationPurityRoleFuncNames[name] {
			continue
		}
		var found string
		visitor := &protectedCallVisitor{
			importAliases:    aliases,
			dotPaths:         dotPaths,
			localUnqualified: localUnqualified,
			protected:        validationPurityProtected,
			toShort:          toShort,
			ordinals:         callSiteOrdinals{},
			onCall: func(_ string, _ *ast.CallExpr, shortLabel string) {
				if found == "" {
					found = shortLabel
				}
			},
		}
		visitor.walkFuncWithPackageScope(fd, pkgScope)
		if found == "" {
			if method := firstValidationPurityForbiddenMethod(fd); method != "" {
				found = method
			}
		}
		if found != "" {
			out[name] = found
		}
	}
	return out
}

func scanValidationPurityWrapperCalls(
	rel string,
	fset *token.FileSet,
	fd *ast.FuncDecl,
	wrappers map[string]string,
	ordinals callSiteOrdinals,
) []convergenceFinding {
	var out []convergenceFinding
	if fd == nil || fd.Body == nil || len(wrappers) == 0 {
		return out
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := unwrapParen(call.Fun).(*ast.Ident)
		if !ok || id.Name == "" {
			return true
		}
		delegates, ok := wrappers[id.Name]
		if !ok {
			return true
		}
		key := fd.Name.Name + "->" + id.Name
		ordinals[key]++
		out = append(out, convergenceFinding{
			Gate: gateValidationPurity, Path: rel,
			Identity:       "call:" + key + "#" + strconv.Itoa(ordinals[key]),
			Classification: classCall,
			Detail:         formatPos(fset, call.Pos()) + " forbidden publication/host wrapper " + id.Name + " (delegates to " + delegates + ") from " + fd.Name.Name,
		})
		return true
	})
	return out
}

// firstValidationPurityForbiddenMethod returns the first forbidden method name
// (direct selector or local method-value alias call) in fd, or "".
func firstValidationPurityForbiddenMethod(fd *ast.FuncDecl) string {
	if fd == nil || fd.Body == nil {
		return ""
	}
	var found string
	_ = walkValidationPurityMethodSurface(fd, func(method string, _ *ast.CallExpr) {
		if found == "" {
			found = method
		}
	})
	return found
}

// scanValidationPurityForbiddenMethodCalls detects direct calls and local
// method-value alias calls to publication/listener-owning method names
// (see validationPurityForbiddenMethodNames).
func scanValidationPurityForbiddenMethodCalls(
	rel string,
	fset *token.FileSet,
	fd *ast.FuncDecl,
	ordinals callSiteOrdinals,
) []convergenceFinding {
	var out []convergenceFinding
	if fd == nil || fd.Body == nil {
		return out
	}
	_ = walkValidationPurityMethodSurface(fd, func(method string, call *ast.CallExpr) {
		key := fd.Name.Name + "->" + method
		ordinals[key]++
		out = append(out, convergenceFinding{
			Gate: gateValidationPurity, Path: rel,
			Identity:       "call:" + key + "#" + strconv.Itoa(ordinals[key]),
			Classification: classCall,
			Detail:         formatPos(fset, call.Pos()) + " forbidden publication method " + method + " from " + fd.Name.Name,
		})
	})
	return out
}

// walkValidationPurityMethodSurface walks fd's body tracking local bindings of
// forbidden method values (publish := mgr.Publish) and reports every call that
// reaches such a method either directly (mgr.Publish(...)) or via alias
// (publish(...)). Nested blocks shadow bindings; assignments update in place.
func walkValidationPurityMethodSurface(fd *ast.FuncDecl, onCall func(method string, call *ast.CallExpr)) map[string]string {
	aliases := map[string]string{}
	if fd == nil || fd.Body == nil {
		return aliases
	}
	var walkStmt func(ast.Stmt, map[string]string)
	walkStmt = func(n ast.Stmt, scope map[string]string) {
		if n == nil {
			return
		}
		switch s := n.(type) {
		case *ast.BlockStmt:
			child := cloneStringMap(scope)
			for _, stmt := range s.List {
				walkStmt(stmt, child)
			}
			for k, v := range child {
				if _, ok := scope[k]; ok {
					scope[k] = v
				}
			}
		case *ast.AssignStmt:
			for _, rhs := range s.Rhs {
				walkExprMethodSurface(rhs, scope, onCall)
			}
			if len(s.Lhs) == len(s.Rhs) {
				for i, lhs := range s.Lhs {
					id, ok := lhs.(*ast.Ident)
					if !ok || id.Name == "_" {
						continue
					}
					if method, ok := forbiddenMethodValue(s.Rhs[i]); ok {
						scope[id.Name] = method
						continue
					}
					if aliased, ok := unwrapParen(s.Rhs[i]).(*ast.Ident); ok {
						if method, ok := scope[aliased.Name]; ok {
							scope[id.Name] = method
							continue
						}
					}
					delete(scope, id.Name)
				}
			} else {
				for _, lhs := range s.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
						delete(scope, id.Name)
					}
				}
			}
		case *ast.DeclStmt:
			gd, ok := s.Decl.(*ast.GenDecl)
			if !ok {
				return
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, val := range vs.Values {
					walkExprMethodSurface(val, scope, onCall)
				}
				if len(vs.Names) == len(vs.Values) {
					for i, name := range vs.Names {
						if name == nil || name.Name == "_" {
							continue
						}
						if method, ok := forbiddenMethodValue(vs.Values[i]); ok {
							scope[name.Name] = method
							continue
						}
						delete(scope, name.Name)
					}
				} else {
					for _, name := range vs.Names {
						if name != nil && name.Name != "_" {
							delete(scope, name.Name)
						}
					}
				}
			}
		case *ast.ExprStmt:
			walkExprMethodSurface(s.X, scope, onCall)
		case *ast.ReturnStmt:
			for _, r := range s.Results {
				walkExprMethodSurface(r, scope, onCall)
			}
		case *ast.DeferStmt:
			walkExprMethodSurface(s.Call, scope, onCall)
		case *ast.GoStmt:
			walkExprMethodSurface(s.Call, scope, onCall)
		case *ast.IfStmt:
			if s.Init != nil {
				walkStmt(s.Init, scope)
			}
			walkExprMethodSurface(s.Cond, scope, onCall)
			if s.Body != nil {
				walkStmt(s.Body, scope)
			}
			if s.Else != nil {
				walkStmt(s.Else, scope)
			}
		case *ast.ForStmt:
			if s.Init != nil {
				walkStmt(s.Init, scope)
			}
			walkExprMethodSurface(s.Cond, scope, onCall)
			if s.Body != nil {
				walkStmt(s.Body, scope)
			}
			if s.Post != nil {
				walkStmt(s.Post, scope)
			}
		case *ast.RangeStmt:
			walkExprMethodSurface(s.X, scope, onCall)
			if s.Body != nil {
				walkStmt(s.Body, scope)
			}
		case *ast.SwitchStmt:
			if s.Init != nil {
				walkStmt(s.Init, scope)
			}
			walkExprMethodSurface(s.Tag, scope, onCall)
			if s.Body != nil {
				walkStmt(s.Body, scope)
			}
		case *ast.TypeSwitchStmt:
			if s.Init != nil {
				walkStmt(s.Init, scope)
			}
			if s.Body != nil {
				walkStmt(s.Body, scope)
			}
		case *ast.SelectStmt:
			if s.Body != nil {
				walkStmt(s.Body, scope)
			}
		case *ast.CaseClause:
			for _, e := range s.List {
				walkExprMethodSurface(e, scope, onCall)
			}
			for _, cs := range s.Body {
				walkStmt(cs, scope)
			}
		case *ast.CommClause:
			if s.Comm != nil {
				walkStmt(s.Comm, scope)
			}
			for _, cs := range s.Body {
				walkStmt(cs, scope)
			}
		case *ast.LabeledStmt:
			walkStmt(s.Stmt, scope)
		}
	}
	// Function body statements share one scope (like protectedCallVisitor.walkBlock).
	for _, stmt := range fd.Body.List {
		walkStmt(stmt, aliases)
	}
	return aliases
}

func walkExprMethodSurface(expr ast.Expr, scope map[string]string, onCall func(method string, call *ast.CallExpr)) {
	if expr == nil {
		return
	}
	switch e := expr.(type) {
	case *ast.CallExpr:
		walkExprMethodSurface(e.Fun, scope, onCall)
		for _, a := range e.Args {
			walkExprMethodSurface(a, scope, onCall)
		}
		fun := unwrapParen(e.Fun)
		if sel, ok := fun.(*ast.SelectorExpr); ok && sel.Sel != nil && validationPurityForbiddenMethodNames[sel.Sel.Name] {
			onCall(sel.Sel.Name, e)
			return
		}
		if id, ok := fun.(*ast.Ident); ok {
			if method, ok := scope[id.Name]; ok {
				onCall(method, e)
			}
		}
	case *ast.ParenExpr:
		walkExprMethodSurface(e.X, scope, onCall)
	case *ast.SelectorExpr:
		walkExprMethodSurface(e.X, scope, onCall)
	case *ast.UnaryExpr:
		walkExprMethodSurface(e.X, scope, onCall)
	case *ast.BinaryExpr:
		walkExprMethodSurface(e.X, scope, onCall)
		walkExprMethodSurface(e.Y, scope, onCall)
	case *ast.IndexExpr:
		walkExprMethodSurface(e.X, scope, onCall)
		walkExprMethodSurface(e.Index, scope, onCall)
	case *ast.SliceExpr:
		walkExprMethodSurface(e.X, scope, onCall)
		walkExprMethodSurface(e.Low, scope, onCall)
		walkExprMethodSurface(e.High, scope, onCall)
		walkExprMethodSurface(e.Max, scope, onCall)
	case *ast.StarExpr:
		walkExprMethodSurface(e.X, scope, onCall)
	case *ast.KeyValueExpr:
		walkExprMethodSurface(e.Key, scope, onCall)
		walkExprMethodSurface(e.Value, scope, onCall)
	case *ast.CompositeLit:
		for _, elt := range e.Elts {
			walkExprMethodSurface(elt, scope, onCall)
		}
	case *ast.TypeAssertExpr:
		walkExprMethodSurface(e.X, scope, onCall)
	case *ast.FuncLit:
		// Isolated scope: method-value aliases inside the lit do not escape,
		// matching protectedCallVisitor's FuncLit isolation.
		if e.Body != nil {
			iso := &ast.FuncDecl{Name: &ast.Ident{Name: "funcLit"}, Body: e.Body}
			_ = walkValidationPurityMethodSurface(iso, onCall)
		}
	}
}

func forbiddenMethodValue(expr ast.Expr) (string, bool) {
	sel, ok := unwrapParen(expr).(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || !validationPurityForbiddenMethodNames[sel.Sel.Name] {
		return "", false
	}
	return sel.Sel.Name, true
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
