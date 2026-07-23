package archtest

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
)

const (
	importCoreConfig    = "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	importRuntimebundle = "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	importStdhttp       = "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	importLipruntime    = "github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
	pathReloadHost      = "internal/infra/runtimebundle/reload_host.go"
	pathConfigSourceEff = "internal/infra/configsource/effective.go"
	pathConfigEffective = "internal/core/config/effective_load.go"
)

// reloadContractTypeNames are type names that form the mirrored reload vocabulary.
// The gate ratchets to one canonical package by shrinking the allowlist.
var reloadContractTypeNames = map[string]bool{
	"TriggerKind":    true,
	"ReloadTrigger":  true,
	"ResultCategory": true,
	"ReloadResult":   true,
	"HistoryEntry":   true,
	"ReloadStatus":   true,
}

var reloadContractConstPrefixes = []string{"Trigger", "Result"}

var reloadContractVarNames = map[string]bool{
	"AllResultCategories": true,
}

var runtimeConvergenceProtected = protectedSymbolSet{
	"runtimebundle.Build":     true,
	"stdhttp.RunWithRuntime":  true,
	"requestPlaneAsBuilt":     true,
}

var hostPathProtected = protectedSymbolSet{
	"runtimebundle.BuildBootstrap":  true,
	"runtimebundle.AttachReloadHost": true,
	"runtimebundle.Build":           true,
	"stdhttp.RunWithRuntime":        true,
	"stdhttp.RunWithGenerationHost": true,
	"lipruntime.Build":              true,
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
	visitor := &protectedCallVisitor{
		importAliases:    aliases,
		dotPaths:         dotImportedProtectedPaths(f, runtimeConvergenceDotPaths),
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
		visitor.walkFunc(fd)
	}
	return out, nil
}

// scanReloadContractSource detects reload trigger/category/result/history/status
// vocabulary declarations.
func scanReloadContractSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
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
				if !ok || ts.Name == nil || !reloadContractTypeNames[ts.Name.Name] {
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
			if isRuntimebundlePath(rel) || isLipruntimePath(rel) {
				out = append(out, convergenceFinding{
					Gate: gateHostPath, Path: rel, Identity: "func:Build",
					Classification: classDeclaration,
					Detail:         formatPos(fset, fd.Name.Pos()) + " compatibility Build declaration",
				})
			}
		}
	}

	ordinals := callSiteOrdinals{}
	toShort := func(resolved string) (string, bool) {
		if hostPathProtected[resolved] {
			return resolved, true
		}
		return "", false
	}
	visitor := &protectedCallVisitor{
		importAliases:    aliases,
		dotPaths:         dotImportedProtectedPaths(f, hostPathDotPaths),
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
		visitor.walkFunc(fd)
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
	visitor := &protectedCallVisitor{
		importAliases:    aliases,
		dotPaths:         dotImportedProtectedPaths(f, configLoadDotPaths),
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
		visitor.walkFunc(fd)
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
