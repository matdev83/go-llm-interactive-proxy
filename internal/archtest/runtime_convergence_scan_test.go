package archtest

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
)

const (
	importCoreConfig         = "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	importRuntimebundle      = "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	importStdhttp            = "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	importLipruntime         = "github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
	importSDKConfigReload    = "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
	pathCanonicalReloadContr = "pkg/lipsdk/configreload/"
	pathReloadHost           = "internal/infra/runtimebundle/reload_host.go"
	pathConfigSourceEff      = "internal/infra/configsource/effective.go"
	pathConfigEffective      = "internal/core/config/effective_load.go"
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

var hostPathProtected = protectedSymbolSet{
	"runtimebundle.BuildBootstrap":   true,
	"runtimebundle.AttachReloadHost": true,
	"runtimebundle.Build":            true,
	"stdhttp.RunWithRuntime":         true,
	"stdhttp.RunWithGenerationHost":  true,
	"lipruntime.Build":               true,
}

var runtimeConvergenceDotPaths = map[string]bool{
	importRuntimebundle: true,
	importStdhttp:       true,
}

var hostPathDotPaths = map[string]bool{
	importRuntimebundle: true,
	importStdhttp:       true,
	importLipruntime:    true,
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

// scanHostPathSource detects host builders and two-step attachment declarations/calls.
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
		localUnqualified["BuildBootstrap"] = "runtimebundle.BuildBootstrap"
		localUnqualified["AttachReloadHost"] = "runtimebundle.AttachReloadHost"
		localUnqualified["Build"] = "runtimebundle.Build"
	}
	if isStdhttpPath(rel) {
		localUnqualified["RunWithRuntime"] = "stdhttp.RunWithRuntime"
		localUnqualified["RunWithGenerationHost"] = "stdhttp.RunWithGenerationHost"
	}
	if isLipruntimePath(rel) {
		localUnqualified["Build"] = "lipruntime.Build"
	}

	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Recv != nil {
			continue // skip methods; avoid unrelated Build methods
		}
		switch fd.Name.Name {
		case "BuildBootstrap", "AttachReloadHost":
			if isRuntimebundlePath(rel) {
				out = append(out, convergenceFinding{
					Gate: gateHostPath, Path: rel, Identity: "func:" + fd.Name.Name,
					Classification: classDeclaration,
					Detail:         formatPos(fset, fd.Name.Pos()) + " host-path declaration",
				})
			}
		case "RunWithRuntime", "RunWithGenerationHost":
			if isStdhttpPath(rel) {
				out = append(out, convergenceFinding{
					Gate: gateHostPath, Path: rel, Identity: "func:" + fd.Name.Name,
					Classification: classDeclaration,
					Detail:         formatPos(fset, fd.Name.Pos()) + " host-path declaration",
				})
			}
		case "Build":
			if isRuntimebundlePath(rel) {
				out = append(out, convergenceFinding{
					Gate: gateHostPath, Path: rel, Identity: "func:Build",
					Classification: classDeclaration,
					Detail:         formatPos(fset, fd.Name.Pos()) + " compatibility Build declaration",
				})
			}
			// Thin pkg/lipruntime.Build that delegates to BuildHost is not an old
			// host-path declaration; call-site scanning still catches BuildBootstrap
			// / AttachReloadHost aliases and wrappers inside it.
		}
	}

	ordinals := callSiteOrdinals{}
	toShort := func(resolved string) (string, bool) {
		if hostPathProtected[resolved] {
			return resolved, true
		}
		return "", false
	}
	dotPaths := dotImportedProtectedPaths(f, hostPathDotPaths)
	pkgScope := packageScopeProtectedAliases(f, aliases, dotPaths, localUnqualified, hostPathProtected)
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
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		visitor.walkFuncWithPackageScope(fd, pkgScope)
	}
	return out, nil
}

// scanConfigLoadSource detects startup effective-config load owners/call sites.
// Reload-attempt LoadEffective in reload_host.go and shared configsource helpers
// are excluded; ordinary test helpers are excluded by production-file walking.
func scanConfigLoadSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	switch rel {
	case pathReloadHost, pathConfigSourceEff, pathConfigEffective:
		return nil, nil
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
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Recv != nil {
			continue
		}
		switch fd.Name.Name {
		case "LoadBootstrapEffective", "LoadBootstrapEffectiveWithSource":
			out = append(out, convergenceFinding{
				Gate: gateConfigLoad, Path: rel, Identity: "func:" + fd.Name.Name,
				Classification: classOwner,
				Detail:         formatPos(fset, fd.Name.Pos()) + " startup effective-load owner",
			})
		}
	}

	ordinals := callSiteOrdinals{}
	toShort := func(resolved string) (string, bool) {
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
	// Include both short and package-qualified forms in the protected set for alias tracking.
	prot := protectedSymbolSet{
		"config.LoadEffective":                           true,
		"runtimebundle.LoadBootstrapEffective":           true,
		"runtimebundle.LoadBootstrapEffectiveWithSource": true,
		"LoadBootstrapEffective":                         true,
		"LoadBootstrapEffectiveWithSource":               true,
	}
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
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		visitor.walkFuncWithPackageScope(fd, pkgScope)
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
