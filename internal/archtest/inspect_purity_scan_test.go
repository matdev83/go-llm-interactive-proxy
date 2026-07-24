package archtest

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// gateInspectPurity guards Task 5.3's Inspect invariant: CLI routes/inventory
// drivers and the focused runtimebundle Inspect operation graph must never
// reach broad bootstrap/host/process/generation owners (or aliased/renamed
// wrappers of those owners).
const gateInspectPurity = "inspect_purity"

// inspectRoleFuncNames are Inspect driving adapters and composition-root
// operations. Exact CLI names alone are insufficient; runtimebundle Inspect
// entrypoints and prepareInspect are scanned wherever they appear.
var inspectRoleFuncNames = map[string]bool{
	"runRoutesCommand":    true,
	"runInventoryCommand": true,
	"InspectRoutes":       true,
	"inspectRoutes":       true,
	"InspectInventory":    true,
	"inspectInventory":    true,
	"prepareInspect":      true,
}

// inspectPurityProtected are broad/runtime-owning symbols Inspect must not reach.
var inspectPurityProtected = protectedSymbolSet{
	"runtimebundle.BuildBootstrap":     true,
	"runtimebundle.BuildHost":          true,
	"runtimebundle.AttachReloadHost":   true,
	"runtimebundle.NewProcessServices": true,
	"runtimebundle.CompileGeneration":  true,
	"runtimebundle.Build":              true,
	"runtimehost.NewManager":           true,
	"stdhttp.RunWithRuntime":           true,
	"stdhttp.RunWithGenerationHost":    true,
}

var inspectPurityDotPaths = map[string]bool{
	importRuntimebundle: true,
	importRuntimehost:   true,
	importStdhttp:       true,
}

// inspectPurityForbiddenImports are ownership packages that production
// inspect.go must not import. Focused load/registry/projection deps stay allowed.
var inspectPurityForbiddenImports = map[string]string{
	importRuntimehost: "runtimehost",
	importStdhttp:     "stdhttp",
	importTracing:     "tracing",
	"log/slog":        "slog",
}

// scanInspectPuritySource detects forbidden broad/runtime-owning calls from
// Inspect-role functions (CLI + composition-root operations), including local
// and package-scope aliases and obvious same-package wrappers whose bodies
// delegate to a forbidden owner. Production inspect.go also rejects ownership
// package imports.
func scanInspectPuritySource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	aliases := importAliasToPath(f)
	var out []convergenceFinding

	if rel == pathInspectOps {
		out = append(out, scanInspectPurityForbiddenImports(rel, fset, f)...)
	}

	localUnqualified := inspectPurityLocalUnqualified(rel)
	dotPaths := dotImportedProtectedPaths(f, inspectPurityDotPaths)
	pkgScope := packageScopeProtectedAliases(f, aliases, dotPaths, localUnqualified, inspectPurityProtected)
	funcs := samePackageFuncDecls(f)
	wrappers := inspectPurityWrapperDelegates(funcs, aliases, dotPaths, localUnqualified, pkgScope)

	ordinals := callSiteOrdinals{}
	toShort := func(resolved string) (string, bool) {
		if inspectPurityProtected[resolved] {
			return resolved, true
		}
		return "", false
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Name == nil || fd.Recv != nil {
			continue
		}
		if !inspectPurityRoleFunc(rel, fd.Name.Name) {
			continue
		}
		visitor := &protectedCallVisitor{
			importAliases:    aliases,
			dotPaths:         dotPaths,
			localUnqualified: localUnqualified,
			protected:        inspectPurityProtected,
			toShort:          toShort,
			ordinals:         ordinals,
			onCall: func(identity string, call *ast.CallExpr, shortLabel string) {
				out = append(out, convergenceFinding{
					Gate: gateInspectPurity, Path: rel,
					Identity:       identity,
					Classification: classCall,
					Detail:         formatPos(fset, call.Pos()) + " forbidden broad call " + shortLabel + " from " + fd.Name.Name,
				})
			},
		}
		visitor.walkFuncWithPackageScope(fd, pkgScope)
		out = append(out, scanInspectPurityWrapperCalls(rel, fset, fd, wrappers, ordinals)...)
	}
	return out, nil
}

func inspectPurityRoleFunc(rel, name string) bool {
	if inspectRoleFuncNames[name] {
		return true
	}
	// Conservative file rule: every function in production inspect.go is
	// Inspect-role surface (helpers added there must stay focused too).
	return rel == pathInspectOps
}

func inspectPurityLocalUnqualified(rel string) map[string]string {
	out := map[string]string{}
	if !isRuntimebundlePath(rel) {
		return out
	}
	for label := range inspectPurityProtected {
		if strings.HasPrefix(label, "runtimebundle.") {
			out[strings.TrimPrefix(label, "runtimebundle.")] = label
		}
	}
	return out
}

func scanInspectPurityForbiddenImports(rel string, fset *token.FileSet, f *ast.File) []convergenceFinding {
	var out []convergenceFinding
	if f == nil {
		return out
	}
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		ipath := strings.Trim(imp.Path.Value, `"`)
		short, ok := inspectPurityForbiddenImports[ipath]
		if !ok {
			continue
		}
		out = append(out, convergenceFinding{
			Gate: gateInspectPurity, Path: rel,
			Identity:       "import:" + short,
			Classification: classDeclaration,
			Detail:         formatPos(fset, imp.Path.Pos()) + " inspect.go must not import ownership package " + ipath,
		})
	}
	return out
}

func samePackageFuncDecls(f *ast.File) map[string]*ast.FuncDecl {
	out := map[string]*ast.FuncDecl{}
	if f == nil {
		return out
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Recv != nil || fd.Body == nil {
			continue
		}
		out[fd.Name.Name] = fd
	}
	return out
}

// inspectPurityWrapperDelegates maps same-package function names whose bodies
// reach a forbidden owner to that owner's short label (one-hop, obvious wrappers).
func inspectPurityWrapperDelegates(
	funcs map[string]*ast.FuncDecl,
	aliases map[string]string,
	dotPaths []string,
	localUnqualified map[string]string,
	pkgScope *aliasScope,
) map[string]string {
	out := map[string]string{}
	toShort := func(resolved string) (string, bool) {
		if inspectPurityProtected[resolved] {
			return resolved, true
		}
		return "", false
	}
	for name, fd := range funcs {
		if inspectRoleFuncNames[name] {
			// Role funcs are scanned at their own call sites; do not reclassify
			// them as wrappers for peer role calls.
			continue
		}
		var found string
		visitor := &protectedCallVisitor{
			importAliases:    aliases,
			dotPaths:         dotPaths,
			localUnqualified: localUnqualified,
			protected:        inspectPurityProtected,
			toShort:          toShort,
			ordinals:         callSiteOrdinals{},
			onCall: func(_ string, _ *ast.CallExpr, shortLabel string) {
				if found == "" {
					found = shortLabel
				}
			},
		}
		visitor.walkFuncWithPackageScope(fd, pkgScope)
		if found != "" {
			out[name] = found
		}
	}
	return out
}

func scanInspectPurityWrapperCalls(
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
			Gate: gateInspectPurity, Path: rel,
			Identity:       "call:" + key + "#" + strconv.Itoa(ordinals[key]),
			Classification: classCall,
			Detail:         formatPos(fset, call.Pos()) + " forbidden broad wrapper " + id.Name + " (delegates to " + delegates + ") from " + fd.Name.Name,
		})
		return true
	})
	return out
}
