package archtest

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	importCoreConfig         = "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	importRuntimebundle      = "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	importRuntimehost        = "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	importTracing            = "github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	importStdhttp            = "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	importLipruntime         = "github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
	importNet                = "net"
	importNetHTTP            = "net/http"
	importSDKConfigReload    = "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
	pathCanonicalReloadContr = "pkg/lipsdk/configreload/"
	pathReloadHost           = "internal/infra/runtimebundle/reload_host.go"
	pathInspectOps           = "internal/infra/runtimebundle/inspect.go"
	pathConfigSourceEff      = "internal/infra/configsource/effective.go"
	pathConfigEffective      = "internal/core/config/effective_load.go"
	pathBootstrapEffective   = "internal/infra/runtimebundle/bootstrap_effective.go"
	pathHostBuild            = "internal/infra/runtimebundle/host_build.go"
	pathCmdServeCommand      = "cmd/lipstd/command.go"
	pathLipruntimeBuild      = "pkg/lipruntime/build.go"
)

// reloadContractTypeNames are type names that form the mirrored reload vocabulary
// for findings (defined types and non-canonical aliases). Bare Trigger/Result/Status
// are collision-prone globally; they are findings only inside reload-contract packages
// (see reloadContractNeutralTypeNames + isReloadContractPackage).
var reloadContractTypeNames = map[string]bool{
	"TriggerKind":    true,
	"ReloadTrigger":  true,
	"ResultCategory": true,
	"ReloadResult":   true,
	"HistoryEntry":   true,
	"ReloadStatus":   true,
}

// reloadContractNeutralTypeNames are short canonical type spellings that collide with
// unrelated packages. They are scanned only when isReloadContractPackage is true.
var reloadContractNeutralTypeNames = map[string]bool{
	"Trigger": true,
	"Result":  true,
	"Status":  true,
}

// reloadCanonicalAliasTarget maps a reload vocabulary LHS name to the only
// approved canonical RHS selector name in pkg/lipsdk/configreload.
// Compatibility names (ReloadTrigger/ReloadResult/ReloadStatus) must alias the
// short canonical types (Trigger/Result/Status), not a same-spelling legacy type.
var reloadCanonicalAliasTarget = map[string]string{
	"TriggerKind":    "TriggerKind",
	"ResultCategory": "ResultCategory",
	"Trigger":        "Trigger",
	"ReloadTrigger":  "Trigger",
	"Result":         "Result",
	"ReloadResult":   "Result",
	"Status":         "Status",
	"ReloadStatus":   "Status",
	"HistoryEntry":   "HistoryEntry",
}

var reloadContractConstPrefixes = []string{"Trigger", "Result"}

var reloadContractVarNames = map[string]bool{
	"AllResultCategories": true,
}

var runtimeConvergenceProtected = protectedSymbolSet{
	"runtimebundle.Build":    true,
	"stdhttp.RunWithRuntime": true,
	"requestPlaneAsBuilt":    true,
}

// hostPathProtected tracks the sole canonical Host builder. Task 4.3's
// stdhttp.RunWithGenerationHost remains a serving adapter (not a startup
// builder) and is intentionally excluded here.
var hostPathProtected = protectedSymbolSet{
	"runtimebundle.BuildHost": true,
}

// hostPathAllowedCallKeys are the only production BuildHost call identities
// permitted after Task 5.5 (zero exceptions).
var hostPathAllowedCallKeys = map[string]bool{
	pathCmdServeCommand + "|call:runServeCommand->runtimebundle.BuildHost#1": true,
	pathLipruntimeBuild + "|call:Build->runtimebundle.BuildHost#1":           true,
}

var runtimeConvergenceDotPaths = map[string]bool{
	importRuntimebundle: true,
	importStdhttp:       true,
}

var hostPathDotPaths = map[string]bool{
	importRuntimebundle: true,
}

var configLoadDotPaths = map[string]bool{
	importCoreConfig:    true,
	importRuntimebundle: true,
}

// scanRuntimeConvergenceSource detects requestPlaneAsBuilt / Built-rehydration
// adapters and inventoried old-path calls (RunWithRuntime, runtimebundle.Build),
// including practical local function-value aliases.
func scanRuntimeConvergenceSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	aliases := importAliasToPath(f)
	var out []convergenceFinding
	out = append(out, scanDotImportFindings(gateRuntimeConvergence, rel, fset, f, runtimeConvergenceDotPaths)...)

	localUnqualified := map[string]string{}
	if isStdhttpPath(rel) {
		localUnqualified["requestPlaneAsBuilt"] = "requestPlaneAsBuilt"
		localUnqualified["RunWithRuntime"] = "stdhttp.RunWithRuntime"
	}
	if isRuntimebundlePath(rel) {
		localUnqualified["Build"] = "runtimebundle.Build"
	}

	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil {
			continue
		}
		switch fd.Name.Name {
		case "requestPlaneAsBuilt":
			out = append(out, convergenceFinding{
				Gate: gateRuntimeConvergence, Path: rel, Identity: "func:requestPlaneAsBuilt",
				Classification: classAdapter,
				Detail:         formatPos(fset, fd.Name.Pos()) + " requestPlaneAsBuilt declaration",
			})
		case "RunWithRuntime":
			if isStdhttpPath(rel) {
				out = append(out, convergenceFinding{
					Gate: gateRuntimeConvergence, Path: rel, Identity: "func:RunWithRuntime",
					Classification: classDeclaration,
					Detail:         formatPos(fset, fd.Name.Pos()) + " RunWithRuntime declaration",
				})
			}
		case "Build":
			if fd.Recv == nil && isRuntimebundlePath(rel) {
				out = append(out, convergenceFinding{
					Gate: gateRuntimeConvergence, Path: rel, Identity: "func:Build",
					Classification: classDeclaration,
					Detail:         formatPos(fset, fd.Name.Pos()) + " runtimebundle.Build declaration",
				})
			}
		}
		if fd.Name.Name != "requestPlaneAsBuilt" && isBuiltRehydrationAdapter(fd, aliases) {
			out = append(out, convergenceFinding{
				Gate: gateRuntimeConvergence, Path: rel, Identity: "func:" + fd.Name.Name,
				Classification: classAdapter,
				Detail:         formatPos(fset, fd.Name.Pos()) + " RequestPlane→Built rehydration adapter",
			})
		}
	}

	ordinals := callSiteOrdinals{}
	toShort := func(resolved string) (string, bool) {
		switch resolved {
		case "runtimebundle.Build":
			return "runtimebundle.Build", true
		case "stdhttp.RunWithRuntime":
			return "stdhttp.RunWithRuntime", true
		case "requestPlaneAsBuilt":
			return "requestPlaneAsBuilt", true
		default:
			if strings.HasSuffix(resolved, ".requestPlaneAsBuilt") {
				return "requestPlaneAsBuilt", true
			}
			return "", false
		}
	}
	dotPaths := dotImportedProtectedPaths(f, runtimeConvergenceDotPaths)
	pkgScope := packageScopeProtectedAliases(f, aliases, dotPaths, localUnqualified, runtimeConvergenceProtected)
	visitor := &protectedCallVisitor{
		importAliases:    aliases,
		dotPaths:         dotPaths,
		localUnqualified: localUnqualified,
		protected:        runtimeConvergenceProtected,
		toShort:          toShort,
		ordinals:         ordinals,
		onCall: func(identity string, call *ast.CallExpr, shortLabel string) {
			out = append(out, convergenceFinding{
				Gate: gateRuntimeConvergence, Path: rel,
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
		visitor.walkFuncWithPackageScope(fd, pkgScope)
	}
	return out, nil
}

// scanReloadContractSource detects reload trigger/category/result/history/status
// vocabulary declarations. The canonical owner pkg/lipsdk/configreload is exempt.
// Type aliases are exempt only when they directly select the approved canonical
// target through a pkg/lipsdk/configreload import (including renamed imports).
// Const/var selector re-exports from that package remain exempt.
// Neutral names Trigger/Result/Status are findings only in reload-contract packages
// (package name or path segment configreload), still subject to the alias exemption.
func scanReloadContractSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	if strings.HasPrefix(rel, pathCanonicalReloadContr) {
		return nil, nil
	}
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	pkgName := ""
	if f.Name != nil {
		pkgName = f.Name.Name
	}
	aliases := importAliasToPath(f)
	var out []convergenceFinding
	for _, decl := range f.Decls {
		d, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		switch d.Tok {
		case token.TYPE:
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil || !isReloadContractTypeName(ts.Name.Name, rel, pkgName) {
					continue
				}
				if ts.Assign.IsValid() {
					if typeAliasIsCanonicalReload(ts.Name.Name, ts.Type, aliases) {
						continue
					}
					out = append(out, convergenceFinding{
						Gate: gateReloadContract, Path: rel, Identity: "type:" + ts.Name.Name,
						Classification: classDeclaration,
						Detail:         formatPos(fset, ts.Name.Pos()) + " reload vocabulary type alias",
					})
					continue
				}
				out = append(out, convergenceFinding{
					Gate: gateReloadContract, Path: rel, Identity: "type:" + ts.Name.Name,
					Classification: classDeclaration,
					Detail:         formatPos(fset, ts.Name.Pos()) + " reload vocabulary type",
				})
			}
		case token.CONST:
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if valueSpecSelectsCanonicalReload(vs, aliases) {
					continue
				}
				for _, name := range vs.Names {
					if name == nil || !reloadConstName(name.Name) {
						continue
					}
					out = append(out, convergenceFinding{
						Gate: gateReloadContract, Path: rel, Identity: "const:" + name.Name,
						Classification: classDeclaration,
						Detail:         formatPos(fset, name.Pos()) + " reload vocabulary const",
					})
				}
			}
		case token.VAR:
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if valueSpecSelectsCanonicalReload(vs, aliases) {
					continue
				}
				for _, name := range vs.Names {
					if name == nil || !reloadContractVarNames[name.Name] {
						continue
					}
					out = append(out, convergenceFinding{
						Gate: gateReloadContract, Path: rel, Identity: "var:" + name.Name,
						Classification: classDeclaration,
						Detail:         formatPos(fset, name.Pos()) + " reload vocabulary var",
					})
				}
			}
		}
	}
	return out, nil
}

// valueSpecSelectsCanonicalReload reports whether every value expression is a
// selector from pkg/lipsdk/configreload (thin re-export, not a mirror).
func valueSpecSelectsCanonicalReload(vs *ast.ValueSpec, aliases map[string]string) bool {
	if vs == nil || len(vs.Values) == 0 {
		return false
	}
	for _, v := range vs.Values {
		sel, ok := v.(*ast.SelectorExpr)
		if !ok || sel.X == nil {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id == nil {
			return false
		}
		if aliases[id.Name] != importSDKConfigReload {
			return false
		}
	}
	return true
}

// typeAliasIsCanonicalReload reports whether a type alias directly selects the
// approved canonical target for name through a pkg/lipsdk/configreload import.
// Local idents, indirect alias chains, builtins, anonymous types, and wrong
// target pairings are not exempt (no name-only or cross-file chain following).
func typeAliasIsCanonicalReload(name string, typ ast.Expr, aliases map[string]string) bool {
	want, ok := reloadCanonicalAliasTarget[name]
	if !ok || typ == nil {
		return false
	}
	sel, ok := typ.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != want {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg == nil {
		return false
	}
	return aliases[pkg.Name] == importSDKConfigReload
}

// scanHostPathSource inventories the Task 5.5 one-canonical-BuildHost graph:
// BuildHost declarations (func/var/const/type), direct/aliased/dot-imported
// calls, package/local aliases, and one-hop wrappers that delegate to BuildHost.
// Production gates assert exactly one declaration at pathHostBuild and exactly
// the two allowed callers (cmd/lipstd.runServeCommand, pkg/lipruntime.Build).
// stdhttp.RunWithGenerationHost is not a startup builder (Task 4.3).
func scanHostPathSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	aliases := importAliasToPath(f)
	var out []convergenceFinding
	out = append(out, scanDotImportFindings(gateHostPath, rel, fset, f, hostPathDotPaths)...)

	localUnqualified := map[string]string{}
	if isRuntimebundlePath(rel) {
		localUnqualified["BuildHost"] = "runtimebundle.BuildHost"
	}

	out = append(out, scanHostPathBuildHostDecls(rel, fset, f)...)

	toShort := func(resolved string) (string, bool) {
		if hostPathProtected[resolved] {
			return resolved, true
		}
		return "", false
	}
	dotPaths := dotImportedProtectedPaths(f, hostPathDotPaths)
	pkgScope := packageScopeProtectedAliases(f, aliases, dotPaths, localUnqualified, hostPathProtected)
	ordinals := callSiteOrdinals{}
	visitor := &protectedCallVisitor{
		importAliases:    aliases,
		dotPaths:         dotPaths,
		localUnqualified: localUnqualified,
		protected:        hostPathProtected,
		toShort:          toShort,
		ordinals:         ordinals,
		onCall: func(identity string, call *ast.CallExpr, shortLabel string) {
			out = append(out, convergenceFinding{
				Gate: gateHostPath, Path: rel,
				Identity:       identity,
				Classification: classCall,
				Detail:         formatPos(fset, call.Pos()) + " call " + shortLabel,
			})
		},
	}

	funcs := samePackageFuncDecls(f)
	wrappers := hostPathBuildHostWrapperDelegates(funcs, aliases, dotPaths, localUnqualified, pkgScope)
	// The two approved production callers are not "extra" Host builders.
	delete(wrappers, "BuildHost")
	if rel == pathLipruntimeBuild {
		delete(wrappers, "Build")
	}
	if rel == pathCmdServeCommand {
		delete(wrappers, "runServeCommand")
	}
	for name, delegates := range wrappers {
		out = append(out, convergenceFinding{
			Gate: gateHostPath, Path: rel,
			Identity:       "wrapper:" + name,
			Classification: classAdapter,
			Detail:         "extra Host builder " + name + " delegates to " + delegates,
		})
	}

	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		visitor.walkFuncWithPackageScope(fd, pkgScope)
		out = append(out, scanHostPathWrapperCalls(rel, fset, fd, wrappers, ordinals)...)
	}

	out = append(out, scanHostPathPackageAliases(rel, fset, f, aliases, dotPaths, localUnqualified)...)
	return out, nil
}

func scanHostPathBuildHostDecls(rel string, fset *token.FileSet, f *ast.File) []convergenceFinding {
	var out []convergenceFinding
	// BuildHost declarations are only meaningful as runtimebundle package-scope
	// symbols (or sneaked under the same name elsewhere as an alternate builder).
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name == nil || d.Recv != nil || d.Name.Name != "BuildHost" {
				continue
			}
			out = append(out, convergenceFinding{
				Gate: gateHostPath, Path: rel, Identity: "func:BuildHost",
				Classification: classDeclaration,
				Detail:         formatPos(fset, d.Name.Pos()) + " BuildHost declaration",
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
						if n == nil || n.Name != "BuildHost" {
							continue
						}
						out = append(out, convergenceFinding{
							Gate: gateHostPath, Path: rel, Identity: kind + ":BuildHost",
							Classification: classDeclaration,
							Detail:         formatPos(fset, n.Pos()) + " BuildHost " + kind + " declaration",
						})
					}
				case *ast.TypeSpec:
					if s.Name == nil || s.Name.Name != "BuildHost" {
						continue
					}
					out = append(out, convergenceFinding{
						Gate: gateHostPath, Path: rel, Identity: "type:BuildHost",
						Classification: classDeclaration,
						Detail:         formatPos(fset, s.Name.Pos()) + " BuildHost type declaration",
					})
				}
			}
		}
	}
	return out
}

func hostPathBuildHostWrapperDelegates(
	funcs map[string]*ast.FuncDecl,
	aliases map[string]string,
	dotPaths []string,
	localUnqualified map[string]string,
	pkgScope *aliasScope,
) map[string]string {
	out := map[string]string{}
	toShort := func(resolved string) (string, bool) {
		if hostPathProtected[resolved] {
			return resolved, true
		}
		return "", false
	}
	for name, fd := range funcs {
		var found string
		visitor := &protectedCallVisitor{
			importAliases:    aliases,
			dotPaths:         dotPaths,
			localUnqualified: localUnqualified,
			protected:        hostPathProtected,
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

func scanHostPathWrapperCalls(
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
		// Direct BuildHost calls are inventoried by protectedCallVisitor.
		if id.Name == "BuildHost" {
			return true
		}
		delegates, ok := wrappers[id.Name]
		if !ok {
			return true
		}
		key := fd.Name.Name + "->" + id.Name
		ordinals[key]++
		out = append(out, convergenceFinding{
			Gate: gateHostPath, Path: rel,
			Identity:       "call:" + key + "#" + strconv.Itoa(ordinals[key]),
			Classification: classCall,
			Detail:         formatPos(fset, call.Pos()) + " Host-builder wrapper " + id.Name + " (delegates to " + delegates + ")",
		})
		return true
	})
	return out
}

func scanHostPathPackageAliases(
	rel string,
	fset *token.FileSet,
	f *ast.File,
	importAliases map[string]string,
	dotPaths []string,
	localUnqualified map[string]string,
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
				if name.Name == "BuildHost" {
					continue // covered as declaration
				}
				resolved, ok := resolveProtectedFuncValue(vs.Values[i], scope, importAliases, dotPaths, localUnqualified, hostPathProtected)
				if !ok {
					continue
				}
				out = append(out, convergenceFinding{
					Gate: gateHostPath, Path: rel,
					Identity:       "alias:" + name.Name,
					Classification: classAdapter,
					Detail:         formatPos(fset, name.Pos()) + " package alias of " + resolved,
				})
			}
		}
	}
	return out
}

// scanConfigLoadSource detects startup effective-config load owners/call sites.
// Reload-attempt LoadEffective in reload_host.go and shared configsource /
// core config internals are narrowly exempt by role (not as arbitrary places
// for startup wrapper owners). The canonical owner file is structurally
// validated: exactly one LoadBootstrapEffectiveWithSource declaration and
// exactly one direct config.LoadEffective call inside it (req: single
// config-load owner, zero-exception after Task 5.5).
//
// LoadBootstrapEffectiveWithSource is protected in every non-canonical
// production file, including other runtimebundle files. Approved BuildHost /
// Validate / Inspect call-scoped passing of the owner identifier is not an
// ast.CallExpr and is therefore not inventoried; direct calls, local/package
// aliases that are invoked, and one-hop wrappers that invoke the owner fail.
func scanConfigLoadSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	switch rel {
	case pathReloadHost, pathConfigSourceEff, pathConfigEffective:
		return nil, nil
	case pathBootstrapEffective:
		return scanCanonicalBootstrapEffectiveOwner(filename, src)
	}

	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	aliases := importAliasToPath(f)
	var out []convergenceFinding
	out = append(out, scanDotImportFindings(gateConfigLoad, rel, fset, f, configLoadDotPaths)...)

	localUnqualified := map[string]string{}
	if isRuntimebundlePath(rel) {
		localUnqualified["LoadBootstrapEffective"] = "LoadBootstrapEffective"
		localUnqualified["LoadBootstrapEffectiveWithSource"] = "LoadBootstrapEffectiveWithSource"
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name == nil || d.Recv != nil {
				continue
			}
			switch d.Name.Name {
			case "LoadBootstrapEffective", "LoadBootstrapEffectiveWithSource":
				out = append(out, convergenceFinding{
					Gate: gateConfigLoad, Path: rel, Identity: "func:" + d.Name.Name,
					Classification: classOwner,
					Detail:         formatPos(fset, d.Name.Pos()) + " startup effective-load owner",
				})
			}
		case *ast.GenDecl:
			kind := task43GenDeclKind(d.Tok)
			if kind == "" {
				continue
			}
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n == nil {
							continue
						}
						switch n.Name {
						case "LoadBootstrapEffective", "LoadBootstrapEffectiveWithSource":
							out = append(out, convergenceFinding{
								Gate: gateConfigLoad, Path: rel, Identity: kind + ":" + n.Name,
								Classification: classOwner,
								Detail:         formatPos(fset, n.Pos()) + " startup effective-load " + kind + " owner",
							})
						}
					}
				case *ast.TypeSpec:
					if s.Name == nil {
						continue
					}
					switch s.Name.Name {
					case "LoadBootstrapEffective", "LoadBootstrapEffectiveWithSource":
						out = append(out, convergenceFinding{
							Gate: gateConfigLoad, Path: rel, Identity: "type:" + s.Name.Name,
							Classification: classOwner,
							Detail:         formatPos(fset, s.Name.Pos()) + " startup effective-load type owner",
						})
					}
				}
			}
		}
	}

	ordinals := callSiteOrdinals{}
	toShort := configLoadToShort
	prot := configLoadProtected
	dotPaths := dotImportedProtectedPaths(f, configLoadDotPaths)
	pkgScope := packageScopeProtectedAliases(f, aliases, dotPaths, localUnqualified, prot)
	visitor := &protectedCallVisitor{
		importAliases:    aliases,
		dotPaths:         dotPaths,
		localUnqualified: localUnqualified,
		protected:        prot,
		toShort:          toShort,
		ordinals:         ordinals,
		onCall: func(identity string, call *ast.CallExpr, shortLabel string) {
			out = append(out, convergenceFinding{
				Gate: gateConfigLoad, Path: rel,
				Identity:       identity,
				Classification: classCall,
				Detail:         formatPos(fset, call.Pos()) + " startup " + shortLabel,
			})
		},
	}

	funcs := samePackageFuncDecls(f)
	wrappers := configLoadWrapperDelegates(funcs, aliases, dotPaths, localUnqualified, pkgScope)
	for name, delegates := range wrappers {
		out = append(out, convergenceFinding{
			Gate: gateConfigLoad, Path: rel,
			Identity:       "wrapper:" + name,
			Classification: classOwner,
			Detail:         "startup load wrapper " + name + " delegates to " + delegates,
		})
	}

	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		visitor.walkFuncWithPackageScope(fd, pkgScope)
	}
	out = append(out, scanConfigLoadPackageAliases(rel, fset, f, aliases, dotPaths, localUnqualified)...)
	return out, nil
}

// configLoadProtected is the full set of startup effective-load callables.
// LoadBootstrapEffectiveWithSource is protected everywhere outside the
// canonical owner file (including other runtimebundle production files).
var configLoadProtected = protectedSymbolSet{
	"config.LoadEffective":                           true,
	"runtimebundle.LoadBootstrapEffective":           true,
	"LoadBootstrapEffective":                         true,
	"runtimebundle.LoadBootstrapEffectiveWithSource": true,
	"LoadBootstrapEffectiveWithSource":               true,
}

func configLoadToShort(resolved string) (string, bool) {
	switch resolved {
	case "config.LoadEffective":
		return "config.LoadEffective", true
	case "runtimebundle.LoadBootstrapEffective", "LoadBootstrapEffective":
		return "LoadBootstrapEffective", true
	case "runtimebundle.LoadBootstrapEffectiveWithSource", "LoadBootstrapEffectiveWithSource":
		return "LoadBootstrapEffectiveWithSource", true
	default:
		return "", false
	}
}

func configLoadWrapperDelegates(
	funcs map[string]*ast.FuncDecl,
	aliases map[string]string,
	dotPaths []string,
	localUnqualified map[string]string,
	pkgScope *aliasScope,
) map[string]string {
	out := map[string]string{}
	for name, fd := range funcs {
		switch name {
		case "LoadBootstrapEffective", "LoadBootstrapEffectiveWithSource":
			continue // inventoried as owners, not wrappers
		}
		var found string
		visitor := &protectedCallVisitor{
			importAliases:    aliases,
			dotPaths:         dotPaths,
			localUnqualified: localUnqualified,
			protected:        configLoadProtected,
			toShort:          configLoadToShort,
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

func scanConfigLoadPackageAliases(
	rel string,
	fset *token.FileSet,
	f *ast.File,
	importAliases map[string]string,
	dotPaths []string,
	localUnqualified map[string]string,
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
				switch name.Name {
				case "LoadBootstrapEffective", "LoadBootstrapEffectiveWithSource":
					continue // covered as owner declarations
				}
				resolved, ok := resolveProtectedFuncValue(vs.Values[i], scope, importAliases, dotPaths, localUnqualified, configLoadProtected)
				if !ok {
					continue
				}
				out = append(out, convergenceFinding{
					Gate: gateConfigLoad, Path: rel,
					Identity:       "alias:" + name.Name,
					Classification: classOwner,
					Detail:         formatPos(fset, name.Pos()) + " package alias of " + resolved,
				})
			}
		}
	}
	return out
}

// scanCanonicalBootstrapEffectiveOwner structurally validates the sole
// approved startup effective-loader file. It inventories the canonical owner
// declaration and its one direct config.LoadEffective call, and rejects
// duplicate owners, no-source wrappers, aliases, private LoadEffective helpers,
// and extra direct loads in the same file.
func scanCanonicalBootstrapEffectiveOwner(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	aliases := importAliasToPath(f)
	var out []convergenceFinding

	const canonicalOwner = "LoadBootstrapEffectiveWithSource"
	ownerDecls := 0
	var ownerFunc *ast.FuncDecl
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name == nil || d.Recv != nil {
				continue
			}
			switch d.Name.Name {
			case canonicalOwner:
				ownerDecls++
				ownerFunc = d
				out = append(out, convergenceFinding{
					Gate: gateConfigLoad, Path: rel, Identity: "func:" + canonicalOwner,
					Classification: classOwner,
					Detail:         formatPos(fset, d.Name.Pos()) + " canonical startup effective-load owner",
				})
			case "LoadBootstrapEffective":
				out = append(out, convergenceFinding{
					Gate: gateConfigLoad, Path: rel, Identity: "func:LoadBootstrapEffective",
					Classification: classOwner,
					Detail:         formatPos(fset, d.Name.Pos()) + " deleted no-source LoadBootstrapEffective wrapper",
				})
			}
		case *ast.GenDecl:
			kind := task43GenDeclKind(d.Tok)
			if kind == "" {
				continue
			}
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n == nil {
							continue
						}
						switch n.Name {
						case canonicalOwner, "LoadBootstrapEffective":
							out = append(out, convergenceFinding{
								Gate: gateConfigLoad, Path: rel, Identity: kind + ":" + n.Name,
								Classification: classOwner,
								Detail:         formatPos(fset, n.Pos()) + " non-func startup load owner " + kind,
							})
						}
					}
				case *ast.TypeSpec:
					if s.Name == nil {
						continue
					}
					switch s.Name.Name {
					case canonicalOwner, "LoadBootstrapEffective":
						out = append(out, convergenceFinding{
							Gate: gateConfigLoad, Path: rel, Identity: "type:" + s.Name.Name,
							Classification: classOwner,
							Detail:         formatPos(fset, s.Name.Pos()) + " startup load type owner",
						})
					}
				}
			}
		}
	}
	if ownerDecls == 0 {
		out = append(out, convergenceFinding{
			Gate: gateConfigLoad, Path: rel, Identity: "missing:LoadBootstrapEffectiveWithSource",
			Classification: classOwner,
			Detail:         "canonical startup effective-load owner declaration missing",
		})
	}
	if ownerDecls > 1 {
		out = append(out, convergenceFinding{
			Gate: gateConfigLoad, Path: rel, Identity: "duplicate:LoadBootstrapEffectiveWithSource",
			Classification: classOwner,
			Detail:         "canonical startup effective-load owner must be declared exactly once",
		})
	}

	localUnqualified := map[string]string{
		"LoadBootstrapEffective":           "LoadBootstrapEffective",
		"LoadBootstrapEffectiveWithSource": "LoadBootstrapEffectiveWithSource",
	}
	prot := configLoadProtected
	toShort := configLoadToShort
	dotPaths := dotImportedProtectedPaths(f, configLoadDotPaths)
	pkgScope := packageScopeProtectedAliases(f, aliases, dotPaths, localUnqualified, prot)

	// Reject package aliases that create another load owner under a new name.
	out = append(out, scanConfigLoadPackageAliases(rel, fset, f, aliases, dotPaths, localUnqualified)...)

	// Private helpers that call config.LoadEffective or call/alias-invoke the
	// canonical owner are extra owners; only the canonical owner body may
	// perform the single approved config.LoadEffective call.
	funcs := samePackageFuncDecls(f)
	wrappers := configLoadWrapperDelegates(funcs, aliases, dotPaths, localUnqualified, pkgScope)
	for name, delegates := range wrappers {
		out = append(out, convergenceFinding{
			Gate: gateConfigLoad, Path: rel, Identity: "wrapper:" + name,
			Classification: classOwner,
			Detail:         "private startup load wrapper " + name + " delegates to " + delegates,
		})
	}

	if ownerFunc != nil && ownerFunc.Body != nil {
		ordinals := callSiteOrdinals{}
		visitor := &protectedCallVisitor{
			importAliases:    aliases,
			dotPaths:         dotPaths,
			localUnqualified: localUnqualified,
			protected:        prot,
			toShort:          toShort,
			ordinals:         ordinals,
			onCall: func(identity string, call *ast.CallExpr, shortLabel string) {
				if shortLabel != "config.LoadEffective" {
					return
				}
				out = append(out, convergenceFinding{
					Gate: gateConfigLoad, Path: rel,
					Identity:       identity,
					Classification: classCall,
					Detail:         formatPos(fset, call.Pos()) + " direct " + shortLabel,
				})
			},
		}
		visitor.walkFuncWithPackageScope(ownerFunc, pkgScope)
	}

	return out, nil
}

func isBuiltRehydrationAdapter(fd *ast.FuncDecl, aliases map[string]string) bool {
	if fd == nil || fd.Type == nil || fd.Type.Results == nil || len(fd.Type.Results.List) != 1 {
		return false
	}
	if !resultsLookLikeBuiltPtr(fd.Type.Results.List[0].Type, aliases) {
		return false
	}
	if fd.Type.Params == nil {
		return false
	}
	for _, p := range fd.Type.Params.List {
		if typeLooksLikeRequestPlane(p.Type, aliases) {
			return true
		}
	}
	return false
}

func resultsLookLikeBuiltPtr(expr ast.Expr, aliases map[string]string) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	switch t := star.X.(type) {
	case *ast.Ident:
		return t.Name == "Built"
	case *ast.SelectorExpr:
		pkg, ok := t.X.(*ast.Ident)
		if !ok || t.Sel == nil || t.Sel.Name != "Built" {
			return false
		}
		path := aliases[pkg.Name]
		return path == importRuntimebundle || pkg.Name == "runtimebundle"
	default:
		return false
	}
}

func typeLooksLikeRequestPlane(expr ast.Expr, aliases map[string]string) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "RequestPlane"
	case *ast.SelectorExpr:
		pkg, ok := t.X.(*ast.Ident)
		if !ok || t.Sel == nil || t.Sel.Name != "RequestPlane" {
			return false
		}
		path := aliases[pkg.Name]
		return path == importRuntimebundle || pkg.Name == "runtimebundle"
	default:
		return false
	}
}

func reloadConstName(name string) bool {
	for _, p := range reloadContractConstPrefixes {
		if strings.HasPrefix(name, p) && name != p {
			return true
		}
	}
	return false
}

// isReloadContractPackage reports whether a file clearly belongs to a reload-contract
// package: Go package name configreload and/or a path segment named configreload.
// Used to scope collision-prone neutral type names (Trigger/Result/Status).
func isReloadContractPackage(rel, pkgName string) bool {
	if pkgName == "configreload" {
		return true
	}
	for _, seg := range strings.Split(slashPath(rel), "/") {
		if seg == "configreload" {
			return true
		}
	}
	return false
}

// isReloadContractTypeName reports whether name is a reload vocabulary type that
// should produce findings in this file. Global vocabulary names always qualify;
// neutral short names qualify only inside reload-contract packages.
func isReloadContractTypeName(name, rel, pkgName string) bool {
	if reloadContractTypeNames[name] {
		return true
	}
	return reloadContractNeutralTypeNames[name] && isReloadContractPackage(rel, pkgName)
}

func isRuntimebundlePath(rel string) bool {
	rel = slashPath(rel)
	return strings.Contains(rel, "/runtimebundle/") || strings.HasPrefix(rel, "internal/infra/runtimebundle/")
}

func isStdhttpPath(rel string) bool {
	rel = slashPath(rel)
	return strings.Contains(rel, "/stdhttp/") || strings.HasPrefix(rel, "internal/stdhttp/")
}

func isLipruntimePath(rel string) bool {
	rel = slashPath(rel)
	return strings.Contains(rel, "/lipruntime/") || strings.HasPrefix(rel, "pkg/lipruntime/")
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func slashPath(p string) string {
	return filepath.ToSlash(p)
}
