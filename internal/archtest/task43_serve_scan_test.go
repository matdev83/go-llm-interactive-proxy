package archtest

import (
	"go/ast"
	"go/token"
	"sort"
	"strings"
)

// Task 4.3 gate identifiers. These certify the production data-plane serve
// boundary after Task 4.2 deleted RunWithRuntime and App-owned HTTP serve
// lifecycle: exactly one supported serve API remains, and deleted serve/
// closer-release symbols cannot reappear. Task 4.4 consolidates broader
// deleted-symbol allowlist retirement; these gates stay focused on serving.
const (
	gateTask43SoleServeAPI   = "task43_sole_serve_api"
	gateTask43DeletedServe   = "task43_deleted_serve_symbols"
	gateTask43AppOwnedServe  = "task43_app_owned_serve"
	gateTask43StaleTestNames = "task43_stale_supported_test_names"
)

// task43SoleAllowedServeAPI is the only production data-plane serve entrypoint
// permitted in internal/stdhttp after Task 4.3.
const task43SoleAllowedServeAPI = "RunWithGenerationHost"

// task43DeletedServeDecls are package-scope stdhttp symbols deleted in Task 4.2
// that must not reappear as production declarations (req 3.3, 4.6-4.7).
var task43DeletedServeDecls = map[string]bool{
	"RunWithRuntime":             true,
	"NewStandardHandler":         true,
	"releaseBuiltResources":      true,
	"runClosers":                 true,
	"standardHTTPInputFromBuilt": true,
}

// scanTask43SoleServeAPISource inventories package-scope data-plane serve
// entrypoints in stdhttp. A serve entrypoint is a top-level func/var/const/type
// named RunWithGenerationHost (canonical) or any other obvious serve-like
// name (RunWith*, Serve*, Run*Server, Start*Server/StartHTTP*, Listen*Serve*).
// Composition helpers (Compose*/prepare*/Mount*) and the unexported
// listenAndServe seam are excluded. Findings are emitted for every such decl
// so the production gate can assert the set equals exactly {RunWithGenerationHost}.
func scanTask43SoleServeAPISource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	if !isStdhttpPath(rel) || strings.Contains(rel, "/stdhttp/admin/") {
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
			if d.Name == nil || d.Recv != nil {
				continue
			}
			name := d.Name.Name
			if !isTask43DataPlaneServeDecl(name) {
				continue
			}
			out = append(out, convergenceFinding{
				Gate: gateTask43SoleServeAPI, Path: rel, Identity: "func:" + name,
				Classification: classDeclaration,
				Detail:         formatPos(fset, d.Name.Pos()) + " production data-plane serve declaration",
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
						if n == nil || !ast.IsExported(n.Name) || !isTask43DataPlaneServeDecl(n.Name) {
							continue
						}
						out = append(out, convergenceFinding{
							Gate: gateTask43SoleServeAPI, Path: rel, Identity: kind + ":" + n.Name,
							Classification: classDeclaration,
							Detail:         formatPos(fset, n.Pos()) + " production data-plane serve " + kind + " declaration",
						})
					}
				case *ast.TypeSpec:
					if s.Name == nil || !ast.IsExported(s.Name.Name) || !isTask43DataPlaneServeDecl(s.Name.Name) {
						continue
					}
					out = append(out, convergenceFinding{
						Gate: gateTask43SoleServeAPI, Path: rel, Identity: "type:" + s.Name.Name,
						Classification: classDeclaration,
						Detail:         formatPos(fset, s.Name.Pos()) + " production data-plane serve type declaration",
					})
				}
			}
		}
	}
	return out, nil
}

func task43GenDeclKind(tok token.Token) string {
	switch tok {
	case token.VAR:
		return "var"
	case token.CONST:
		return "const"
	case token.TYPE:
		return "type"
	default:
		return ""
	}
}

func isTask43DataPlaneServeDecl(name string) bool {
	if name == "" || name == "listenAndServe" {
		return false
	}
	if name == task43SoleAllowedServeAPI {
		return true
	}
	// Composition / mount / stack helpers are not data-plane serve entrypoints.
	if strings.HasPrefix(name, "Compose") ||
		strings.HasPrefix(name, "prepare") || strings.HasPrefix(name, "Prepare") ||
		strings.HasPrefix(name, "Mount") ||
		strings.HasPrefix(name, "stack") || strings.HasPrefix(name, "Stack") {
		return false
	}
	if strings.HasPrefix(name, "RunWith") {
		return true
	}
	if strings.HasPrefix(name, "Serve") {
		return true
	}
	if strings.HasPrefix(name, "Run") && strings.Contains(name, "Server") {
		return true
	}
	if strings.HasPrefix(name, "Start") && (strings.Contains(name, "Server") || strings.HasPrefix(name, "StartHTTP")) {
		return true
	}
	if strings.HasPrefix(name, "Listen") && strings.Contains(name, "Serve") {
		return true
	}
	return false
}

// scanTask43DeletedServeSource detects reintroduction of deleted serve /
// Built-release / generic closer-release declarations in stdhttp production
// code (and calls to those symbols anywhere in production). Package-scope
// func/var/const/type declarations under the exact deleted names are rejected;
// methods remain outside this package-scope gate.
func scanTask43DeletedServeSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	aliases := importAliasToPath(f)
	var out []convergenceFinding

	if isStdhttpPath(rel) && !strings.Contains(rel, "/stdhttp/admin/") {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Name == nil || d.Recv != nil || !task43DeletedServeDecls[d.Name.Name] {
					continue
				}
				out = append(out, convergenceFinding{
					Gate: gateTask43DeletedServe, Path: rel, Identity: "func:" + d.Name.Name,
					Classification: classDeclaration,
					Detail:         formatPos(fset, d.Name.Pos()) + " deleted serve/lifecycle symbol reintroduced",
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
							if n == nil || !task43DeletedServeDecls[n.Name] {
								continue
							}
							out = append(out, convergenceFinding{
								Gate: gateTask43DeletedServe, Path: rel, Identity: kind + ":" + n.Name,
								Classification: classDeclaration,
								Detail:         formatPos(fset, n.Pos()) + " deleted serve/lifecycle " + kind + " reintroduced",
							})
						}
					case *ast.TypeSpec:
						if s.Name == nil || !task43DeletedServeDecls[s.Name.Name] {
							continue
						}
						out = append(out, convergenceFinding{
							Gate: gateTask43DeletedServe, Path: rel, Identity: "type:" + s.Name.Name,
							Classification: classDeclaration,
							Detail:         formatPos(fset, s.Name.Pos()) + " deleted serve/lifecycle type reintroduced",
						})
					}
				}
			}
		}
	}

	sameHTTP := isStdhttpPath(rel)
	ordinals := callSiteOrdinals{}
	localUnqualified := map[string]string{}
	if sameHTTP {
		for name := range task43DeletedServeDecls {
			localUnqualified[name] = "stdhttp." + name
		}
	}
	prot := protectedSymbolSet{}
	for name := range task43DeletedServeDecls {
		prot["stdhttp."+name] = true
		prot[name] = true
	}
	toShort := func(resolved string) (string, bool) {
		base := resolved
		if i := strings.LastIndex(resolved, "."); i >= 0 {
			base = resolved[i+1:]
		}
		if task43DeletedServeDecls[base] {
			return "stdhttp." + base, true
		}
		return "", false
	}
	visitor := &protectedCallVisitor{
		importAliases:    aliases,
		dotPaths:         dotImportedProtectedPaths(f, map[string]bool{importStdhttp: true}),
		localUnqualified: localUnqualified,
		protected:        prot,
		toShort:          toShort,
		ordinals:         ordinals,
		onCall: func(identity string, call *ast.CallExpr, shortLabel string) {
			out = append(out, convergenceFinding{
				Gate: gateTask43DeletedServe, Path: rel,
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

// scanTask43AppOwnedServeSource detects runtime.App methods that own HTTP
// serve lifecycle (Serve*/Listen*Serve*/Run*HTTP|Server|DataPlane|…,
// Start*HTTP|Server, Host*Serve). Plugin Start/Shutdown stay allowed.
// Scoped to internal/core/runtime App type declarations.
func scanTask43AppOwnedServeSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	if !isCoreRuntimePath(rel) {
		return nil, nil
	}
	fset, f, err := parseGoSource(filename, src)
	if err != nil {
		return nil, err
	}
	var out []convergenceFinding
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Recv == nil || len(fd.Recv.List) != 1 {
			continue
		}
		if !recvIsRuntimeApp(fd.Recv.List[0].Type) {
			continue
		}
		if !isTask43AppOwnedServeMethod(fd.Name.Name) {
			continue
		}
		out = append(out, convergenceFinding{
			Gate: gateTask43AppOwnedServe, Path: rel,
			Identity:       "method:App." + fd.Name.Name,
			Classification: classOwner,
			Detail:         formatPos(fset, fd.Name.Pos()) + " App-owned HTTP serve lifecycle method",
		})
	}
	return out, nil
}

func isTask43AppOwnedServeMethod(name string) bool {
	switch name {
	case "Start", "Shutdown":
		return false
	}
	if strings.HasPrefix(name, "Serve") {
		return true
	}
	if strings.HasPrefix(name, "Listen") && strings.Contains(name, "Serve") {
		return true
	}
	if strings.Contains(name, "AndServe") {
		return true
	}
	if strings.HasPrefix(name, "Host") && strings.Contains(name, "Serve") {
		return true
	}
	if strings.HasPrefix(name, "Start") && (strings.Contains(name, "HTTP") || strings.Contains(name, "Server") || strings.Contains(name, "Serve")) {
		return true
	}
	if strings.HasPrefix(name, "RunWith") {
		return true
	}
	if strings.HasPrefix(name, "Run") &&
		(strings.Contains(name, "HTTP") ||
			strings.Contains(name, "Server") ||
			strings.Contains(name, "Serve") ||
			strings.Contains(name, "DataPlane") ||
			strings.Contains(name, "Runtime")) {
		return true
	}
	return false
}

func isCoreRuntimePath(rel string) bool {
	rel = slashPath(rel)
	return strings.HasPrefix(rel, "internal/core/runtime/") || rel == "internal/core/runtime"
}

func recvIsRuntimeApp(expr ast.Expr) bool {
	for {
		switch t := expr.(type) {
		case *ast.StarExpr:
			expr = t.X
		case *ast.Ident:
			return t.Name == "App"
		default:
			return false
		}
	}
}

// scanTask43StaleSupportedTestNamesSource flags live stdhttp behavior tests
// whose function names still advertise deleted RunWithRuntime / NewStandardHandler
// seams. Detector/synthetic fixtures under internal/archtest are out of scope
// (this scanner only walks stdhttp test files via the caller).
func scanTask43StaleSupportedTestNamesSource(filename, src string) ([]convergenceFinding, error) {
	rel := slashPath(filename)
	if !isStdhttpRootTestPath(rel) || !strings.HasSuffix(rel, "_test.go") {
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
		name := fd.Name.Name
		if !strings.HasPrefix(name, "Test") {
			continue
		}
		if !strings.Contains(name, "RunWithRuntime") && !strings.Contains(name, "NewStandardHandler") {
			continue
		}
		out = append(out, convergenceFinding{
			Gate: gateTask43StaleTestNames, Path: rel, Identity: "func:" + name,
			Classification: classDeclaration,
			Detail:         formatPos(fset, fd.Name.Pos()) + " supported test name still advertises deleted serve/composition seam",
		})
	}
	return out, nil
}

// collectTask43SoleServeIdentities returns sorted unique func:Name identities
// from sole-serve findings (helper for the exactly-one production assertion).
func collectTask43SoleServeIdentities(fs []convergenceFinding) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range fs {
		if f.Gate != gateTask43SoleServeAPI {
			continue
		}
		if seen[f.Identity] {
			continue
		}
		seen[f.Identity] = true
		out = append(out, f.Identity)
	}
	sort.Strings(out)
	return out
}
