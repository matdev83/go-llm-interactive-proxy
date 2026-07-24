package archtest

import (
	"go/ast"
	"go/token"
	"strings"
)

// Task 4.1 gate identifiers. These prove callers/tests have migrated off
// compatibility Build/Built surfaces before Task 4.2/4.3 delete the producers.
// After Task 4.2/4.4 there is no scheduled-producer exemption: production
// Build/Built is unconditionally forbidden.
const (
	gateTask41BuildCall              = "task41_build_call"
	gateTask41BuiltCarrier           = "task41_built_carrier"
	gateTask41TestLegacyCaller       = "task41_test_legacy_caller"
	gateTask41ReplacementAggregate   = "task41_replacement_aggregate"
	gateTask41LifecycleComposeHelper = "task41_lifecycle_compose_helper"
)

// scanTask41BuildCallSource detects production calls to compatibility
// runtimebundle.Build. Same-package unqualified Build calls inside
// runtimebundle count; the func Build declaration is detected by Task 4.2.
func scanTask41BuildCallSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	aliases := importAliasToPath(f)
	dotRB := fileHasDotImport(f, importRuntimebundle)
	samePkg := isRuntimebundlePath(rel)
	var out []convergenceFinding
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isRuntimebundleBuildCall(call.Fun, aliases, dotRB) &&
			!(samePkg && isUnqualifiedIdentCall(call.Fun, "Build")) {
			return true
		}
		out = append(out, convergenceFinding{
			Gate:           gateTask41BuildCall,
			Path:           rel,
			Identity:       "call:Build@" + formatPos(fset, call.Pos()),
			Classification: classCall,
			Detail:         formatPos(fset, call.Pos()) + " production calls compatibility runtimebundle.Build",
		})
		return true
	})
	return out, nil
}

func isUnqualifiedIdentCall(fun ast.Expr, name string) bool {
	id, ok := fun.(*ast.Ident)
	return ok && id.Name == name
}

// scanTask41BuiltCarrierSource detects production fields/results/params that
// carry runtimebundle.Built. Inside the runtimebundle package, unqualified
// Built type references count. The Built type declaration itself is owned by
// Task 4.2's type-decl gate.
func scanTask41BuiltCarrierSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	aliases := importAliasToPath(f)
	dotRB := fileHasDotImport(f, importRuntimebundle)
	samePkg := isRuntimebundlePath(rel)
	// Treat same-package Built idents like a dot-import of runtimebundle.Built.
	effectiveDot := dotRB || samePkg
	var out []convergenceFinding
	seen := map[string]bool{}
	emit := func(identity string, pos token.Pos, detail string) {
		if identity == "" || seen[identity] {
			return
		}
		seen[identity] = true
		out = append(out, convergenceFinding{
			Gate:           gateTask41BuiltCarrier,
			Path:           rel,
			Identity:       identity,
			Classification: classAdapter,
			Detail:         formatPos(fset, pos) + " " + detail,
		})
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name == nil || !declContainsRuntimebundleBuilt(d, aliases, effectiveDot) {
				continue
			}
			emit(funcDeclStructuralIdentity(d), d.Name.Pos(), "production declaration carries runtimebundle.Built")
		case *ast.GenDecl:
			if d.Tok != token.TYPE && d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name == nil {
						continue
					}
					// The Built type declaration itself is Task 4.2's concern.
					if samePkg && s.Name.Name == "Built" {
						continue
					}
					if !declContainsRuntimebundleBuilt(s, aliases, effectiveDot) {
						continue
					}
					emit("type:"+s.Name.Name, s.Name.Pos(), "production type carries runtimebundle.Built")
				case *ast.ValueSpec:
					if !declContainsRuntimebundleBuilt(s, aliases, effectiveDot) {
						continue
					}
					for _, n := range s.Names {
						if n == nil {
							continue
						}
						emit("var:"+n.Name, n.Pos(), "production var carries runtimebundle.Built")
					}
				}
			}
		}
	}
	return out, nil
}

// scanTask41TestLegacyCallerSource detects non-fixture tests that still call
// Build, construct Built, call NewStandardHandler with Built, or RunWithRuntime.
// Same-package unqualified calls/types are in scope via the file path package
// boundary. String-literal detector fixtures are naturally ignored by the AST;
// there is no filename bypass — a live call in a detector-named file must fail.
func scanTask41TestLegacyCallerSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	if !strings.HasSuffix(rel, "_test.go") {
		return nil, nil
	}
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	aliases := importAliasToPath(f)
	dotRB := fileHasDotImport(f, importRuntimebundle)
	dotHTTP := fileHasDotImport(f, importStdhttp)
	stdAlias := packageAlias(aliases, importStdhttp)
	sameRB := isRuntimebundlePath(rel)
	sameHTTP := isStdhttpRootTestPath(rel)
	effectiveDotRB := dotRB || sameRB
	var out []convergenceFinding
	seen := map[string]bool{}
	emit := func(identity string, pos token.Pos, detail string) {
		if identity == "" || seen[identity] {
			return
		}
		seen[identity] = true
		out = append(out, convergenceFinding{
			Gate:           gateTask41TestLegacyCaller,
			Path:           rel,
			Identity:       identity,
			Classification: classCall,
			Detail:         formatPos(fset, pos) + " " + detail,
		})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if isRuntimebundleBuildCall(node.Fun, aliases, dotRB) ||
				(sameRB && isUnqualifiedIdentCall(node.Fun, "Build")) {
				emit("call:Build@"+formatPos(fset, node.Pos()), node.Pos(), "test calls compatibility runtimebundle.Build")
			}
			if isNamedSelectorCall(node.Fun, stdAlias, dotHTTP, "RunWithRuntime") ||
				(sameHTTP && isUnqualifiedIdentCall(node.Fun, "RunWithRuntime")) {
				emit("call:RunWithRuntime@"+formatPos(fset, node.Pos()), node.Pos(), "test invokes RunWithRuntime")
			}
			if isNamedSelectorCall(node.Fun, stdAlias, dotHTTP, "NewStandardHandler") ||
				(sameHTTP && isUnqualifiedIdentCall(node.Fun, "NewStandardHandler")) {
				emit("call:NewStandardHandler@"+formatPos(fset, node.Pos()), node.Pos(), "test calls NewStandardHandler (Built compatibility)")
			}
		case *ast.CompositeLit:
			if exprIsRuntimebundleBuilt(node.Type, aliases, effectiveDotRB) {
				emit("lit:Built@"+formatPos(fset, node.Pos()), node.Pos(), "test constructs runtimebundle.Built")
			}
		case *ast.FuncDecl:
			if node.Name != nil && declContainsRuntimebundleBuilt(node, aliases, effectiveDotRB) {
				emit("decl:"+funcDeclStructuralIdentity(node), node.Name.Pos(), "test declares runtimebundle.Built dependency")
			}
		case *ast.GenDecl:
			if node.Tok != token.TYPE && node.Tok != token.VAR {
				return true
			}
			for _, spec := range node.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name != nil && declContainsRuntimebundleBuilt(s, aliases, effectiveDotRB) {
						emit("type:"+s.Name.Name, s.Name.Pos(), "test type carries runtimebundle.Built")
					}
				case *ast.ValueSpec:
					if declContainsRuntimebundleBuilt(s, aliases, effectiveDotRB) {
						for _, name := range s.Names {
							if name == nil {
								continue
							}
							emit("var:"+name.Name, name.Pos(), "test var carries runtimebundle.Built")
						}
					}
				}
			}
		}
		return true
	})
	return out, nil
}

// isStdhttpRootTestPath reports root stdhttp package tests (not admin/contract subpackages).
func isStdhttpRootTestPath(rel string) bool {
	rel = slashPath(rel)
	if !strings.HasPrefix(rel, "internal/stdhttp/") {
		return false
	}
	rest := strings.TrimPrefix(rel, "internal/stdhttp/")
	return !strings.Contains(rest, "/")
}

// scanTask41ReplacementAggregateSource detects new test helpers that mirror
// Built as a broad dependency bag. A struct matching the Built-surface field
// threshold fails regardless of its name; generic any/map bags fail only when
// combined with a dependency-bag/helper naming shape.
func scanTask41ReplacementAggregateSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	if !strings.HasSuffix(rel, "_test.go") {
		return nil, nil
	}
	// Only composition-root / HTTP test packages are in scope for replacement bags.
	if !isRuntimebundlePath(rel) && !isStdhttpPath(rel) && !strings.HasPrefix(rel, "internal/core/runtime/") {
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
			if !ok || ts.Name == nil {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			name := ts.Name.Name
			mirrors := structMirrorsBuiltFieldSurface(st)
			generic := structHasGenericBagField(st)
			if mirrors || (generic && looksLikeDependencyBagHelper(name)) {
				out = append(out, convergenceFinding{
					Gate:           gateTask41ReplacementAggregate,
					Path:           rel,
					Identity:       "type:" + name,
					Classification: classAdapter,
					Detail:         formatPos(fset, ts.Name.Pos()) + " replacement aggregate mirrors Built / carries generic bag",
				})
			}
		}
	}
	return out, nil
}

func looksLikeDependencyBagHelper(name string) bool {
	lower := strings.ToLower(name)
	for _, needle := range []string{
		"built", "runtimebag", "dependencybag", "testbundle", "legacyruntime",
		"compatruntime", "runtimefields", "dependencyfields", "helperbag",
		"testruntime", "fields",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func structHasGenericBagField(st *ast.StructType) bool {
	for _, f := range st.Fields.List {
		if f.Type == nil {
			continue
		}
		switch t := f.Type.(type) {
		case *ast.InterfaceType:
			if t.Methods == nil || len(t.Methods.List) == 0 {
				return true // any
			}
		case *ast.MapType:
			if id, ok := t.Key.(*ast.Ident); ok && id.Name == "string" {
				if iface, ok := t.Value.(*ast.InterfaceType); ok && (iface.Methods == nil || len(iface.Methods.List) == 0) {
					return true // map[string]any
				}
			}
		}
	}
	return false
}

// structMirrorsBuiltFieldSurface is a coarse check: many Built product fields
// co-located on one test struct indicates a replacement bag. Threshold is
// name-independent so renaming cannot evade the gate.
func structMirrorsBuiltFieldSurface(st *ast.StructType) bool {
	want := map[string]bool{
		"Executor": true, "Store": true, "Closers": true, "PluginRegistry": true,
		"RuntimeSnapshot": true, "Metrics": true, "CatalogRuntime": true,
		"ModelRegistry": true, "ModelRegistryRuntime": true, "TokenAccountingAdmin": true,
		"UsageAuthority": true, "ControlPlaneQueries": true, "ReadinessReport": true,
		"ConcurrencyAuthority": true, "SecureSessionStore": true, "HTTPAuthProviders": true,
	}
	hits := 0
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n != nil && want[n.Name] {
				hits++
			}
		}
	}
	return hits >= 5
}

// scanTask41LifecycleComposeHelperSource rejects test helper functions that
// both compose HTTP and start/shut down an App in the same function body.
// Separate steps in distinct functions (or the test itself) are allowed.
func scanTask41LifecycleComposeHelperSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	if !strings.HasSuffix(rel, "_test.go") {
		return nil, nil
	}
	if !isStdhttpRootTestPath(rel) && !isRuntimebundlePath(rel) {
		return nil, nil
	}
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	var out []convergenceFinding
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Body == nil {
			continue
		}
		// Test entrypoints may explicitly start + compose as separate steps.
		if strings.HasPrefix(fd.Name.Name, "Test") {
			continue
		}
		hasCompose := false
		hasLifecycle := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := callFuncName(call.Fun)
			switch name {
			case "ComposeStandardHTTP", "prepareStandardHandler":
				hasCompose = true
			case "Start", "Shutdown":
				// App lifecycle selectors (app.Start / app.Shutdown).
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel != nil {
					hasLifecycle = true
				}
			}
			return true
		})
		if hasCompose && hasLifecycle {
			out = append(out, convergenceFinding{
				Gate:           gateTask41LifecycleComposeHelper,
				Path:           rel,
				Identity:       "func:" + fd.Name.Name,
				Classification: classAdapter,
				Detail:         formatPos(fset, fd.Name.Pos()) + " test helper combines HTTP composition with App Start/Shutdown",
			})
		}
	}
	return out, nil
}

func callFuncName(fun ast.Expr) string {
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

func isRuntimebundleBuildCall(fun ast.Expr, aliases map[string]string, dotRB bool) bool {
	switch t := fun.(type) {
	case *ast.Ident:
		return dotRB && t.Name == "Build"
	case *ast.SelectorExpr:
		if t.Sel == nil || t.Sel.Name != "Build" {
			return false
		}
		id, ok := t.X.(*ast.Ident)
		if !ok {
			return false
		}
		return aliases[id.Name] == importRuntimebundle
	}
	return false
}

func isNamedSelectorCall(fun ast.Expr, pkgAlias string, dotImport bool, name string) bool {
	switch t := fun.(type) {
	case *ast.Ident:
		return dotImport && t.Name == name
	case *ast.SelectorExpr:
		if t.Sel == nil || t.Sel.Name != name {
			return false
		}
		id, ok := t.X.(*ast.Ident)
		if !ok {
			return false
		}
		return pkgAlias != "" && id.Name == pkgAlias
	}
	return false
}

func packageAlias(aliases map[string]string, importPath string) string {
	for alias, path := range aliases {
		if path == importPath {
			return alias
		}
	}
	return ""
}

func exprIsRuntimebundleBuilt(expr ast.Expr, aliases map[string]string, dotRB bool) bool {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return exprIsRuntimebundleBuilt(t.X, aliases, dotRB)
	case *ast.Ident:
		return dotRB && t.Name == "Built"
	case *ast.SelectorExpr:
		if t.Sel == nil || t.Sel.Name != "Built" {
			return false
		}
		id, ok := t.X.(*ast.Ident)
		if !ok {
			return false
		}
		return aliases[id.Name] == importRuntimebundle
	}
	return false
}
