package archtest

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// gateTask55DeletedBootstrap permanently rejects reintroduction of Task 5.5
// deleted dual-bootstrap / host-attachment symbols under any package-scope
// func/type/var/const form, callable alias, same-package unqualified use, or
// obvious one-hop / two-step wrapper graph (req 4.1-4.9, 10.1-10.4, 11.2).
const gateTask55DeletedBootstrap = "task55_deleted_bootstrap"

// task55DeletedBootstrapNames are exact identifiers deleted at Task 5.5.
// Production declarations and calls under these names must stay zero.
var task55DeletedBootstrapNames = map[string]bool{
	"BuildBootstrap":       true,
	"AttachReloadHost":     true,
	"BootstrapResult":      true,
	"BootstrapMode":        true,
	"BootstrapUnspecified": true,
	"BootstrapServe":       true,
}

// task55DeletedBootstrapCallables are deleted names that were callable entry
// points; call-site / alias / wrapper scanning focuses on these.
var task55DeletedBootstrapCallables = map[string]bool{
	"BuildBootstrap":   true,
	"AttachReloadHost": true,
}

var task55DeletedBootstrapDotPaths = map[string]bool{
	importRuntimebundle: true,
}

func scanTask55DeletedBootstrapSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	aliases := importAliasToPath(f)
	var out []convergenceFinding
	out = append(out, scanDotImportFindings(gateTask55DeletedBootstrap, rel, fset, f, task55DeletedBootstrapDotPaths)...)

	localUnqualified := map[string]string{}
	if isRuntimebundlePath(rel) {
		for name := range task55DeletedBootstrapCallables {
			localUnqualified[name] = "runtimebundle." + name
		}
	}

	out = append(out, scanTask55DeletedBootstrapDecls(rel, fset, f)...)

	prot := protectedSymbolSet{}
	for name := range task55DeletedBootstrapCallables {
		prot["runtimebundle."+name] = true
	}
	toShort := func(resolved string) (string, bool) {
		if prot[resolved] {
			return resolved, true
		}
		return "", false
	}
	dotPaths := dotImportedProtectedPaths(f, task55DeletedBootstrapDotPaths)
	pkgScope := packageScopeProtectedAliases(f, aliases, dotPaths, localUnqualified, prot)
	ordinals := callSiteOrdinals{}
	visitor := &protectedCallVisitor{
		importAliases:    aliases,
		dotPaths:         dotPaths,
		localUnqualified: localUnqualified,
		protected:        prot,
		toShort:          toShort,
		ordinals:         ordinals,
		onCall: func(identity string, call *ast.CallExpr, shortLabel string) {
			out = append(out, convergenceFinding{
				Gate: gateTask55DeletedBootstrap, Path: rel,
				Identity:       identity,
				Classification: classCall,
				Detail:         formatPos(fset, call.Pos()) + " call " + shortLabel,
			})
		},
	}

	funcs := samePackageFuncDecls(f)
	wrappers := task55DeletedBootstrapWrapperDelegates(funcs, aliases, dotPaths, localUnqualified, pkgScope)
	for name, delegates := range wrappers {
		out = append(out, convergenceFinding{
			Gate: gateTask55DeletedBootstrap, Path: rel,
			Identity:       "wrapper:" + name,
			Classification: classAdapter,
			Detail:         "one-hop wrapper " + name + " delegates to " + delegates,
		})
	}

	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		visitor.walkFuncWithPackageScope(fd, pkgScope)
		out = append(out, scanTask55DeletedBootstrapWrapperCalls(rel, fset, fd, wrappers, ordinals)...)
		if twoStep := task55TwoStepBootstrapAttachGraph(fd, wrappers, aliases, dotPaths, localUnqualified, pkgScope); twoStep != "" {
			out = append(out, convergenceFinding{
				Gate: gateTask55DeletedBootstrap, Path: rel,
				Identity:       "twostep:" + fd.Name.Name,
				Classification: classAdapter,
				Detail:         formatPos(fset, fd.Name.Pos()) + " " + twoStep,
			})
		}
	}

	// Package-scope var aliases of deleted callables (beyond bare name decls).
	out = append(out, scanTask55DeletedBootstrapPackageAliases(rel, fset, f, aliases, dotPaths, localUnqualified, prot)...)
	return out, nil
}

func scanTask55DeletedBootstrapDecls(rel string, fset *token.FileSet, f *ast.File) []convergenceFinding {
	// Bare deleted names are runtimebundle compatibility symbols. Unrelated
	// packages may legitimately reuse names like BootstrapMode / BootstrapResult;
	// only runtimebundle production paths inventory exact bare declarations.
	// Cross-package qualified calls/aliases of deleted callables remain gated
	// by the call / wrapper / package-alias scanners below.
	if !isRuntimebundlePath(rel) {
		return nil
	}
	var out []convergenceFinding
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name == nil || d.Recv != nil || !task55DeletedBootstrapNames[d.Name.Name] {
				continue
			}
			out = append(out, convergenceFinding{
				Gate: gateTask55DeletedBootstrap, Path: rel,
				Identity:       "func:" + d.Name.Name,
				Classification: classDeclaration,
				Detail:         formatPos(fset, d.Name.Pos()) + " deleted bootstrap symbol reintroduced",
			})
		case *ast.GenDecl:
			kind := task43GenDeclKind(d.Tok)
			if kind == "" {
				continue
			}
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n == nil || !task55DeletedBootstrapNames[n.Name] {
							continue
						}
						out = append(out, convergenceFinding{
							Gate: gateTask55DeletedBootstrap, Path: rel,
							Identity:       kind + ":" + n.Name,
							Classification: classDeclaration,
							Detail:         formatPos(fset, n.Pos()) + " deleted bootstrap " + kind + " reintroduced",
						})
					}
				case *ast.TypeSpec:
					if s.Name == nil || !task55DeletedBootstrapNames[s.Name.Name] {
						continue
					}
					out = append(out, convergenceFinding{
						Gate: gateTask55DeletedBootstrap, Path: rel,
						Identity:       "type:" + s.Name.Name,
						Classification: classDeclaration,
						Detail:         formatPos(fset, s.Name.Pos()) + " deleted bootstrap type reintroduced",
					})
				}
			}
		}
	}
	return out
}

func scanTask55DeletedBootstrapPackageAliases(
	rel string,
	fset *token.FileSet,
	f *ast.File,
	importAliases map[string]string,
	dotPaths []string,
	localUnqualified map[string]string,
	prot protectedSymbolSet,
) []convergenceFinding {
	var out []convergenceFinding
	scope := newAliasScope(nil)
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
				if task55DeletedBootstrapNames[name.Name] {
					continue // already covered as bare-name decl
				}
				resolved, ok := resolveProtectedFuncValue(vs.Values[i], scope, importAliases, dotPaths, localUnqualified, prot)
				if !ok {
					continue
				}
				out = append(out, convergenceFinding{
					Gate: gateTask55DeletedBootstrap, Path: rel,
					Identity:       "alias:" + name.Name,
					Classification: classAdapter,
					Detail:         formatPos(fset, name.Pos()) + " package alias of deleted " + resolved,
				})
			}
		}
	}
	return out
}

func task55DeletedBootstrapWrapperDelegates(
	funcs map[string]*ast.FuncDecl,
	aliases map[string]string,
	dotPaths []string,
	localUnqualified map[string]string,
	pkgScope *aliasScope,
) map[string]string {
	out := map[string]string{}
	prot := protectedSymbolSet{}
	for name := range task55DeletedBootstrapCallables {
		prot["runtimebundle."+name] = true
	}
	toShort := func(resolved string) (string, bool) {
		if prot[resolved] {
			return resolved, true
		}
		return "", false
	}
	for name, fd := range funcs {
		if task55DeletedBootstrapCallables[name] {
			continue
		}
		var found string
		visitor := &protectedCallVisitor{
			importAliases:    aliases,
			dotPaths:         dotPaths,
			localUnqualified: localUnqualified,
			protected:        prot,
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

func scanTask55DeletedBootstrapWrapperCalls(
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
			Gate: gateTask55DeletedBootstrap, Path: rel,
			Identity:       "call:" + key + "#" + strconv.Itoa(ordinals[key]),
			Classification: classCall,
			Detail:         formatPos(fset, call.Pos()) + " deleted-bootstrap wrapper " + id.Name + " (delegates to " + delegates + ")",
		})
		return true
	})
	return out
}

// task55TwoStepBootstrapAttachGraph detects an equivalent two-step
// "build partial startup then attach reload" body: both a BuildBootstrap
// (or wrapper) and an AttachReloadHost (or wrapper) reachable in one function.
func task55TwoStepBootstrapAttachGraph(
	fd *ast.FuncDecl,
	wrappers map[string]string,
	aliases map[string]string,
	dotPaths []string,
	localUnqualified map[string]string,
	pkgScope *aliasScope,
) string {
	if fd == nil || fd.Body == nil {
		return ""
	}
	var sawBuild, sawAttach bool
	note := func(label string) {
		switch {
		case strings.Contains(label, "BuildBootstrap"):
			sawBuild = true
		case strings.Contains(label, "AttachReloadHost"):
			sawAttach = true
		}
	}
	prot := protectedSymbolSet{
		"runtimebundle.BuildBootstrap":   true,
		"runtimebundle.AttachReloadHost": true,
	}
	toShort := func(resolved string) (string, bool) {
		if prot[resolved] {
			return resolved, true
		}
		return "", false
	}
	visitor := &protectedCallVisitor{
		importAliases:    aliases,
		dotPaths:         dotPaths,
		localUnqualified: localUnqualified,
		protected:        prot,
		toShort:          toShort,
		ordinals:         callSiteOrdinals{},
		onCall: func(_ string, _ *ast.CallExpr, shortLabel string) {
			note(shortLabel)
		},
	}
	visitor.walkFuncWithPackageScope(fd, pkgScope)
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := unwrapParen(call.Fun).(*ast.Ident)
		if !ok {
			return true
		}
		if delegates, ok := wrappers[id.Name]; ok {
			note(delegates)
		}
		return true
	})
	if sawBuild && sawAttach {
		return "two-step BuildBootstrap+AttachReloadHost wrapper graph"
	}
	return ""
}
