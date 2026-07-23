package archtest

import (
	"go/ast"
	"go/token"
	"strings"
)

// Task 4.2 gate identifiers. These are permanent zero-tolerance gates (no
// allowlist): after Built/Build/candidate-legacy-closer deletion, none of
// these symbols or structural equivalents may reappear in production
// runtimebundle code, under any name/receiver/alias.
const (
	gateTask42BuiltTypeDecl          = "task42_built_type_decl"
	gateTask42BuildDecl              = "task42_build_decl"
	gateTask42CandidateCloserFld     = "task42_candidate_closer_field"
	gateTask42LedgerCloserProjection = "task42_ledger_closer_projection"
	gateTask42TestCtorInProd         = "task42_test_ctor_in_production"
)

// scanTask42BuiltTypeDeclSource detects any production declaration of a type
// literally named "Built" inside the runtimebundle package (struct or any
// other type form), regardless of file. There is no scheduled producer left
// after Task 4.2; the declaration itself is forbidden everywhere.
func scanTask42BuiltTypeDeclSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	if !isRuntimebundlePath(rel) {
		return nil, nil
	}
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	var out []convergenceFinding
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name == nil || ts.Name.Name != "Built" {
				continue
			}
			out = append(out, convergenceFinding{
				Gate: gateTask42BuiltTypeDecl, Path: rel, Identity: "type:Built",
				Classification: classDeclaration,
				Detail:         formatPos(fset, ts.Name.Pos()) + " production type named Built reintroduced",
			})
		}
	}
	return out, nil
}

// scanTask42BuildDeclSource detects any production package-scope declaration
// literally named "Build" inside the runtimebundle package: top-level func,
// var, const, or type. Methods (non-nil receiver) are exempt; unrelated
// packages (modelregistry.Build, lipruntime.Build) are out of scope because
// this scanner only fires on files under internal/infra/runtimebundle.
func scanTask42BuildDeclSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	if !isRuntimebundlePath(rel) {
		return nil, nil
	}
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	var out []convergenceFinding
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name == nil || d.Recv != nil || d.Name.Name != "Build" {
				continue
			}
			out = append(out, convergenceFinding{
				Gate: gateTask42BuildDecl, Path: rel, Identity: "func:Build",
				Classification: classDeclaration,
				Detail:         formatPos(fset, d.Name.Pos()) + " compatibility runtimebundle.Build declaration reintroduced",
			})
		case *ast.GenDecl:
			kind := ""
			switch d.Tok {
			case token.VAR:
				kind = "var"
			case token.CONST:
				kind = "const"
			case token.TYPE:
				kind = "type"
			default:
				continue
			}
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n == nil || n.Name != "Build" {
							continue
						}
						out = append(out, convergenceFinding{
							Gate: gateTask42BuildDecl, Path: rel, Identity: kind + ":Build",
							Classification: classDeclaration,
							Detail:         formatPos(fset, n.Pos()) + " compatibility runtimebundle.Build " + kind + " declaration reintroduced",
						})
					}
				case *ast.TypeSpec:
					if s.Name == nil || s.Name.Name != "Build" {
						continue
					}
					out = append(out, convergenceFinding{
						Gate: gateTask42BuildDecl, Path: rel, Identity: "type:Build",
						Classification: classDeclaration,
						Detail:         formatPos(fset, s.Name.Pos()) + " compatibility runtimebundle.Build type declaration reintroduced",
					})
				}
			}
		}
	}
	return out, nil
}

// scanTask42CandidateCloserFieldSource detects aggregate generation-runtime
// closer-list fields. Exact CandidateRuntime rejects every []func() error
// field (exported or not). Renamed generation/candidate owners that carry a
// broad dependency surface plus a closer-list field are rejected structurally.
// ProcessServices.closers and construction-local helpers without a generation-
// owner role remain allowed. Field name Closers on a generation owner is
// always rejected.
func scanTask42CandidateCloserFieldSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	if !isRuntimebundlePath(rel) {
		return nil, nil
	}
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	localTypes, typeAliases := collectLocalTypeMaps(f)
	var out []convergenceFinding
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
			st := resolveStructType(ts, typeAliases, localTypes)
			if st == nil || st.Fields == nil {
				continue
			}
			owner := isTask42GenerationCloserOwner(ts.Name.Name, st, typeAliases, localTypes)
			for _, field := range st.Fields.List {
				isSliceOfFuncErr := isSliceFuncErrorTypeResolved(field.Type, localTypes, typeAliases)
				for _, n := range field.Names {
					if n == nil {
						continue
					}
					if !owner {
						continue
					}
					if n.Name == "Closers" || isSliceOfFuncErr {
						out = append(out, convergenceFinding{
							Gate: gateTask42CandidateCloserFld, Path: rel,
							Identity:       "field:" + ts.Name.Name + "." + n.Name,
							Classification: classDeclaration,
							Detail:         formatPos(fset, n.Pos()) + " generation-runtime aggregate closer field reintroduced",
						})
					}
				}
			}
		}
	}
	return out, nil
}

// isTask42GenerationCloserOwner reports whether a struct occupies the
// CandidateRuntime / generation-runtime owner role for closer-list bans.
// ProcessServices is explicitly exempt (process closer ownership stays valid).
func isTask42GenerationCloserOwner(name string, st *ast.StructType, aliases map[string]ast.Expr, local map[string]*ast.TypeSpec) bool {
	if name == "ProcessServices" {
		return false
	}
	if name == "CandidateRuntime" {
		return true
	}
	return structHasTask42CandidateSurface(st, aliases, local)
}

// structHasTask42CandidateSurface detects a renamed generation/candidate owner
// by ResourceLedger ownership and/or a broad CandidateRuntime-like dependency
// surface (Executor/Store/PluginRegistry/…). Construction-local helpers such as
// startedModelCatalog do not match.
func structHasTask42CandidateSurface(st *ast.StructType, aliases map[string]ast.Expr, local map[string]*ast.TypeSpec) bool {
	if st == nil || st.Fields == nil {
		return false
	}
	var hasLedger bool
	markers := 0
	for _, field := range st.Fields.List {
		typeName := resolveLocalTypeName(field.Type, local, aliases)
		switch typeName {
		case "ResourceLedger", "*ResourceLedger":
			hasLedger = true
		}
		// Pointer forms from resolveLocalTypeName may strip stars; also check raw.
		if raw := exprTypeName(field.Type); raw == "ResourceLedger" || strings.HasSuffix(raw, "ResourceLedger") {
			hasLedger = true
		}
		for _, n := range field.Names {
			if n == nil {
				continue
			}
			switch n.Name {
			case "Ledger":
				hasLedger = true
			case "Executor", "Store", "UpstreamHTTP", "RoutePrefixes", "DecodeAdmission",
				"PluginRegistry", "DatabasePools", "HTTPAuthProviders", "SecureSessionStore",
				"ModelRegistry", "ControlPlaneQueries", "TokenAccountingAdmin":
				markers++
			}
		}
	}
	if hasLedger {
		return true
	}
	return markers >= 3
}

func collectLocalTypeMaps(f *ast.File) (map[string]*ast.TypeSpec, map[string]ast.Expr) {
	localTypes := map[string]*ast.TypeSpec{}
	typeAliases := map[string]ast.Expr{}
	if f == nil {
		return localTypes, typeAliases
	}
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
	return localTypes, typeAliases
}

// isSliceFuncErrorType reports whether expr is exactly []func() error.
func isSliceFuncErrorType(expr ast.Expr) bool {
	arr, ok := expr.(*ast.ArrayType)
	if !ok || arr.Len != nil {
		return false
	}
	return isFuncErrorType(arr.Elt)
}

// isFuncErrorType reports whether expr is exactly func() error.
func isFuncErrorType(expr ast.Expr) bool {
	ft, ok := expr.(*ast.FuncType)
	if !ok {
		return false
	}
	if ft.Params != nil && fieldParamCount(ft.Params) != 0 {
		return false
	}
	if ft.Results == nil || fieldParamCount(ft.Results) != 1 {
		return false
	}
	id, ok := unwrapTypeExpr(ft.Results.List[0].Type).(*ast.Ident)
	return ok && id.Name == "error"
}

// isSliceFuncErrorTypeResolved reports whether expr is []func() error after
// resolving same-file aliases and locally defined named types whose underlying
// type is func() error or a slice thereof. Resolution is cycle-safe and bounded.
func isSliceFuncErrorTypeResolved(expr ast.Expr, local map[string]*ast.TypeSpec, aliases map[string]ast.Expr) bool {
	return isSliceFuncErrorShape(expr, local, aliases, map[string]bool{}, 0)
}

const task42TypeResolveMaxDepth = 16

func isSliceFuncErrorShape(expr ast.Expr, local map[string]*ast.TypeSpec, aliases map[string]ast.Expr, visiting map[string]bool, depth int) bool {
	if expr == nil || depth > task42TypeResolveMaxDepth {
		return false
	}
	if isSliceFuncErrorType(expr) {
		return true
	}
	if arr, ok := expr.(*ast.ArrayType); ok && arr.Len == nil {
		return isFuncErrorShape(arr.Elt, local, aliases, visiting, depth+1)
	}
	name := localIdentName(expr)
	if name == "" {
		return false
	}
	if visiting[name] {
		return false
	}
	visiting[name] = true
	defer delete(visiting, name)

	if alt, ok := aliases[name]; ok {
		return isSliceFuncErrorShape(alt, local, aliases, visiting, depth+1)
	}
	if ts, ok := local[name]; ok && ts.Type != nil {
		return isSliceFuncErrorShape(ts.Type, local, aliases, visiting, depth+1)
	}
	return false
}

func isFuncErrorShape(expr ast.Expr, local map[string]*ast.TypeSpec, aliases map[string]ast.Expr, visiting map[string]bool, depth int) bool {
	if expr == nil || depth > task42TypeResolveMaxDepth {
		return false
	}
	if isFuncErrorType(expr) {
		return true
	}
	name := localIdentName(expr)
	if name == "" {
		return false
	}
	if visiting[name] {
		return false
	}
	visiting[name] = true
	defer delete(visiting, name)

	if alt, ok := aliases[name]; ok {
		return isFuncErrorShape(alt, local, aliases, visiting, depth+1)
	}
	if ts, ok := local[name]; ok && ts.Type != nil {
		return isFuncErrorShape(ts.Type, local, aliases, visiting, depth+1)
	}
	return false
}

func localIdentName(expr ast.Expr) string {
	id, ok := unwrapTypeExpr(expr).(*ast.Ident)
	if !ok {
		return ""
	}
	return id.Name
}

// scanTask42LedgerCloserProjectionSource detects ResourceLedger → []func() error
// compatibility projections: LegacyClosers by name, ResourceLedger methods
// returning []func() error (any name, pointer or value receiver), and top-level
// functions that accept ResourceLedger/*ResourceLedger (including resolvable
// aliases) and return []func() error. Local closer-slice helpers and
// ProcessServices cleanup are not flagged.
func scanTask42LedgerCloserProjectionSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	if !isRuntimebundlePath(rel) {
		return nil, nil
	}
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	localTypes, typeAliases := collectLocalTypeMaps(f)
	var out []convergenceFinding
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Type == nil {
			continue
		}
		if fd.Recv != nil && len(fd.Recv.List) == 1 {
			recv := resolveTask42TypeName(fd.Recv.List[0].Type, typeAliases)
			if fd.Name.Name == "LegacyClosers" {
				out = append(out, convergenceFinding{
					Gate: gateTask42LedgerCloserProjection, Path: rel,
					Identity:       "method:" + recvTypeName(fd.Recv.List[0].Type) + "." + fd.Name.Name,
					Classification: classDeclaration,
					Detail:         formatPos(fset, fd.Name.Pos()) + " legacy closer-list projection method reintroduced",
				})
				continue
			}
			if !isTask42ResourceLedgerType(recv) {
				continue
			}
			if returnsSliceFuncErrorResolved(fd.Type, localTypes, typeAliases) {
				out = append(out, convergenceFinding{
					Gate: gateTask42LedgerCloserProjection, Path: rel,
					Identity:       "method:" + recvTypeName(fd.Recv.List[0].Type) + "." + fd.Name.Name,
					Classification: classDeclaration,
					Detail:         formatPos(fset, fd.Name.Pos()) + " ResourceLedger method returns []func() error (legacy closer-list shape)",
				})
			}
			continue
		}
		if fd.Recv != nil {
			continue
		}
		// Top-level pure projection: ResourceLedger param + []func() error
		// result, without also accepting an existing local closer slice.
		// Construction helpers such as registerStartedCatalogClosers /
		// appendBackendClosers thread (ledger, closers) → closers and must
		// remain allowed.
		if !funcParamsIncludeResourceLedger(fd.Type, typeAliases) {
			continue
		}
		if funcParamsIncludeSliceFuncErrorResolved(fd.Type, localTypes, typeAliases) {
			continue
		}
		if !returnsSliceFuncErrorResolved(fd.Type, localTypes, typeAliases) {
			continue
		}
		out = append(out, convergenceFinding{
			Gate: gateTask42LedgerCloserProjection, Path: rel,
			Identity:       "func:" + fd.Name.Name,
			Classification: classDeclaration,
			Detail:         formatPos(fset, fd.Name.Pos()) + " ResourceLedger→[]func() error projection function reintroduced",
		})
	}
	return out, nil
}

func returnsSliceFuncErrorResolved(ft *ast.FuncType, local map[string]*ast.TypeSpec, aliases map[string]ast.Expr) bool {
	if ft == nil || ft.Results == nil || len(ft.Results.List) != 1 {
		return false
	}
	return isSliceFuncErrorTypeResolved(ft.Results.List[0].Type, local, aliases)
}

func funcParamsIncludeResourceLedger(ft *ast.FuncType, aliases map[string]ast.Expr) bool {
	if ft == nil || ft.Params == nil {
		return false
	}
	for _, p := range ft.Params.List {
		if isTask42ResourceLedgerType(resolveTask42TypeName(p.Type, aliases)) {
			return true
		}
	}
	return false
}

func funcParamsIncludeSliceFuncErrorResolved(ft *ast.FuncType, local map[string]*ast.TypeSpec, aliases map[string]ast.Expr) bool {
	if ft == nil || ft.Params == nil {
		return false
	}
	for _, p := range ft.Params.List {
		if isSliceFuncErrorTypeResolved(p.Type, local, aliases) {
			return true
		}
	}
	return false
}

func resolveTask42TypeName(expr ast.Expr, aliases map[string]ast.Expr) string {
	name := exprTypeName(expr)
	if alt, ok := aliases[name]; ok {
		return exprTypeName(alt)
	}
	// Pointer to alias: *LedgerBag
	if star, ok := unwrapTypeExpr(expr).(*ast.StarExpr); ok {
		inner := exprTypeName(star.X)
		if alt, ok := aliases[inner]; ok {
			resolved := exprTypeName(alt)
			if resolved != "" && !strings.HasPrefix(resolved, "*") {
				return "*" + resolved
			}
			return resolved
		}
	}
	if alt, ok := aliases[strings.TrimPrefix(name, "*")]; ok {
		resolved := exprTypeName(alt)
		if strings.HasPrefix(name, "*") && resolved != "" && !strings.HasPrefix(resolved, "*") {
			return "*" + resolved
		}
		return resolved
	}
	return name
}

func isTask42ResourceLedgerType(name string) bool {
	switch name {
	case "ResourceLedger", "*ResourceLedger":
		return true
	default:
		return false
	}
}

// scanTask42TestCtorInProductionSource detects a test-only constructor whose
// only purpose is exposing ResourceLedger/CandidateRuntime internals (name
// ends in "ForTest" and its signature mentions either type) declared in a
// non-_test.go production file. Such helpers must live in export_test.go.
// Unrelated ForTest helpers (e.g. process-services closer disposal test
// hooks) are out of Task 4.2 scope and intentionally not flagged.
func scanTask42TestCtorInProductionSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	if !isRuntimebundlePath(rel) || strings.HasSuffix(rel, "_test.go") {
		return nil, nil
	}
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	var out []convergenceFinding
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Recv != nil {
			continue
		}
		if !strings.HasSuffix(fd.Name.Name, "ForTest") {
			continue
		}
		if !funcSignatureMentionsLedgerOrCandidate(fd) {
			continue
		}
		out = append(out, convergenceFinding{
			Gate: gateTask42TestCtorInProd, Path: rel, Identity: "func:" + fd.Name.Name,
			Classification: classDeclaration,
			Detail:         formatPos(fset, fd.Name.Pos()) + " test-only ledger/candidate exposer declared in production .go file (move to export_test.go)",
		})
	}
	return out, nil
}

func funcSignatureMentionsLedgerOrCandidate(fd *ast.FuncDecl) bool {
	if fd == nil || fd.Type == nil {
		return false
	}
	mentions := func(fl *ast.FieldList) bool {
		if fl == nil {
			return false
		}
		for _, f := range fl.List {
			if id, ok := unwrapTypeExpr(f.Type).(*ast.Ident); ok &&
				(id.Name == "ResourceLedger" || id.Name == "CandidateRuntime") {
				return true
			}
		}
		return false
	}
	return mentions(fd.Type.Params) || mentions(fd.Type.Results)
}
