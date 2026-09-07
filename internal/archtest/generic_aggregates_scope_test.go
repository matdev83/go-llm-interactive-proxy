package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// archTestModulePath qualifies approved exception identities so a foreign
// package imported under the same local name cannot spoof an approval.
const archTestModulePath = "github.com/matdev83/go-llm-interactive-proxy"

// archPkgFile is one parsed non-test file plus its local import names.
type archPkgFile struct {
	name    string
	file    *ast.File
	imports map[string]string
}

// archPkgScope is the package-wide declaration surface: named structs, every
// declared type name (for alias/defined-type chain resolution), and per-file
// import maps for package identity checks.
type archPkgScope struct {
	files    []*archPkgFile
	structs  map[string]*ast.StructType
	ownerOf  map[string]*archPkgFile
	declared map[string]ast.Expr
	declFile map[string]*archPkgFile
	// aliasOf records whether declared[name] was an alias (type X = ...).
	// Aliases are transparent identities; defined types are opaque.
	aliasOf map[string]bool
}

// archForbiddenFeatureTokens flags per-feature names in generic aggregates.
var archForbiddenFeatureTokens = []string{
	"reasoning",
	"keepwarm",
	"compaction",
	"secretguard",
	"interleavedthinking",
	"interleaved",
	"sessionpolicy",
}

func archForbiddenPkg(importPath string) bool {
	if strings.HasPrefix(importPath, archTestModulePath+"/internal/plugins/features") {
		return true
	}
	if strings.HasPrefix(importPath, archTestModulePath+"/internal/standardplugins/featurehost/") {
		return true
	}
	if strings.Contains(importPath, "secretguardcompose") ||
		strings.Contains(importPath, "compactioncompose") ||
		strings.Contains(importPath, "reasoningcompose") ||
		strings.Contains(importPath, "reasoningreplay") {
		return true
	}
	return false
}

func archFileImports(node *ast.File) map[string]string {
	imports := make(map[string]string, len(node.Imports))
	for _, imp := range node.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		localName := filepath.Base(importPath)
		if imp.Name != nil {
			localName = imp.Name.Name
		}
		imports[localName] = importPath
	}
	return imports
}

func archScopeFromParsed(files map[string]*ast.File) *archPkgScope {
	scope := &archPkgScope{
		structs:  make(map[string]*ast.StructType),
		ownerOf:  make(map[string]*archPkgFile),
		declared: make(map[string]ast.Expr),
		declFile: make(map[string]*archPkgFile),
		aliasOf:  make(map[string]bool),
	}
	for name, node := range files {
		pf := &archPkgFile{name: name, file: node, imports: archFileImports(node)}
		scope.files = append(scope.files, pf)
		for _, decl := range node.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if st, ok := ts.Type.(*ast.StructType); ok {
					if _, exists := scope.structs[ts.Name.Name]; !exists {
						scope.structs[ts.Name.Name] = st
						scope.ownerOf[ts.Name.Name] = pf
					}
				}
				if _, exists := scope.declared[ts.Name.Name]; !exists {
					scope.declared[ts.Name.Name] = ts.Type
					scope.declFile[ts.Name.Name] = pf
					scope.aliasOf[ts.Name.Name] = ts.Assign.IsValid()
				}
			}
		}
	}
	return scope
}

func archParseDir(t *testing.T, dir string) *archPkgScope {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	files := make(map[string]*ast.File)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", name, err)
		}
		files[name] = node
	}
	return archScopeFromParsed(files)
}

func archParseSources(t *testing.T, sources map[string]string) *archPkgScope {
	t.Helper()
	files := make(map[string]*ast.File, len(sources))
	for name, src := range sources {
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", name, err)
		}
		files[name] = node
	}
	return archScopeFromParsed(files)
}

// scanStructForFeatureFields is the single-file entry point used by the
// direct-reference negative fixtures; it delegates to the package scope.
func scanStructForFeatureFields(node *ast.File, structName string, allowedExceptions map[string]string) []string {
	return archScopeFromParsed(map[string]*ast.File{"synthetic.go": node}).scan(structName, allowedExceptions)
}

// splitArchShape unwraps pointer/array layers, returning the shape prefix.
func splitArchShape(expr ast.Expr) (string, ast.Expr) {
	shape := ""
	curr := expr
	for {
		switch t := curr.(type) {
		case *ast.StarExpr:
			shape += "*"
			curr = t.X
		case *ast.ArrayType:
			shape += "[]"
			curr = t.Elt
		default:
			return shape, curr
		}
	}
}

// resolveArchLocal resolves a same-package type name through alias and
// defined-type chains: qualified when the chain ends at another package,
// local when it ends at a same-package declaration.
func (s *archPkgScope) resolveArchLocal(name string) (importPath, typeName string, ok bool) {
	seen := make(map[string]bool)
	curr := name
	for {
		if seen[curr] {
			return "", "", false
		}
		seen[curr] = true
		if _, isStruct := s.structs[curr]; isStruct {
			return "", curr, true
		}
		under, declared := s.declared[curr]
		if !declared {
			return "", "", false
		}
		owner := s.declFile[curr]
		_, base := splitArchShape(under)
		switch b := base.(type) {
		case *ast.SelectorExpr:
			x, ok := b.X.(*ast.Ident)
			if !ok {
				return "", "", false
			}
			p, ok := owner.imports[x.Name]
			if !ok {
				return "", "", false
			}
			return p, b.Sel.Name, true
		case *ast.Ident:
			curr = b.Name
		default:
			return "", curr, true
		}
	}
}

// forbiddenPkgInType reports whether a field type references a forbidden
// feature package, directly or through a same-package chain. Only type
// positions are visited; inline-struct field names are never type references.
func (s *archPkgScope) forbiddenPkgInType(expr ast.Expr, file *archPkgFile) (string, bool) {
	bad := ""
	hit := false
	var visit func(e ast.Expr)
	visit = func(e ast.Expr) {
		if hit || e == nil {
			return
		}
		switch t := e.(type) {
		case *ast.SelectorExpr:
			if x, ok := t.X.(*ast.Ident); ok {
				if p, ok := file.imports[x.Name]; ok && archForbiddenPkg(p) {
					bad, hit = p, true
				}
			}
		case *ast.Ident:
			if p, _, ok := s.resolveArchLocal(t.Name); ok && p != "" && archForbiddenPkg(p) {
				bad, hit = p, true
			} else if badDecl, hitDecl := s.forbiddenInDeclType(t.Name, make(map[string]bool)); hitDecl {
				bad, hit = badDecl, true
			}
		case *ast.StarExpr:
			visit(t.X)
		case *ast.ArrayType:
			visit(t.Elt)
		case *ast.MapType:
			visit(t.Key)
			visit(t.Value)
		case *ast.ChanType:
			visit(t.Value)
		case *ast.StructType:
			if t.Fields != nil {
				for _, f := range t.Fields.List {
					visit(f.Type)
				}
			}
		case *ast.FuncType:
			if t.Params != nil {
				for _, f := range t.Params.List {
					visit(f.Type)
				}
			}
			if t.Results != nil {
				for _, f := range t.Results.List {
					visit(f.Type)
				}
			}
		case *ast.InterfaceType:
			if t.Methods != nil {
				for _, f := range t.Methods.List {
					visit(f.Type)
				}
			}
		}
	}
	visit(expr)
	return bad, hit
}

// parseArchApprovedRef splits an approved exception type into shape, import
// path, and type name ("LocalName" stays same-package).
func parseArchApprovedRef(expected string) (shape, path, name string) {
	rest := expected
	for strings.HasPrefix(rest, "*") || strings.HasPrefix(rest, "[]") {
		if strings.HasPrefix(rest, "*") {
			shape += "*"
			rest = rest[1:]
		} else {
			shape += "[]"
			rest = rest[2:]
		}
	}
	if idx := strings.LastIndex(rest, "."); idx >= 0 && strings.Contains(rest[:idx], "/") {
		return shape, rest[:idx], rest[idx+1:]
	}
	return shape, "", rest
}

// approvedExceptionMatches validates an exception claim by fully-resolved
// package+type+shape identity: the composed pointer/array shape (outer field
// shape plus shapes inside alias declarations) must equal the approved shape
// exactly, and defined types are opaque — a defined type never matches the
// approved reference it wraps. Textual spelling alone is insufficient, so a
// foreign package imported under an approved local name is still rejected.
func (s *archPkgScope) approvedExceptionMatches(expr ast.Expr, file *archPkgFile, expected string) bool {
	expShape, expPath, expName := parseArchApprovedRef(expected)
	actShape, actPath, actName, ok := s.resolveArchFullType(expr, file)
	if !ok {
		return false
	}
	return actShape == expShape && actPath == expPath && actName == expName
}

// namedArchRefs returns same-package candidate type names referenced by a
// field type, unwrapping pointers, arrays, map keys/values, and channels.
func namedArchRefs(expr ast.Expr) []string {
	switch t := expr.(type) {
	case *ast.Ident:
		return []string{t.Name}
	case *ast.StarExpr:
		return namedArchRefs(t.X)
	case *ast.ArrayType:
		return namedArchRefs(t.Elt)
	case *ast.MapType:
		return append(namedArchRefs(t.Key), namedArchRefs(t.Value)...)
	case *ast.ChanType:
		return namedArchRefs(t.Value)
	default:
		return nil
	}
}

// scan checks one aggregate struct with its own exceptions; see scanWithRows.
func (s *archPkgScope) scan(structName string, allowedExceptions map[string]string) []string {
	return s.scanWithRows(structName, map[string]map[string]string{structName: allowedExceptions})
}

// scanWithRows checks the root aggregate plus nested groups from any same-
// package file and inline anonymous groups. Approvals are per-struct: a nested
// struct owning its own exception row keeps its approvals wherever reached.
func (s *archPkgScope) scanWithRows(root string, rows map[string]map[string]string) []string {
	rootST, ok := s.structs[root]
	if !ok {
		return nil
	}
	rootExceptions := rows[root]
	lookupApproval := func(curr, qualName, fieldName string) (string, bool) {
		if row := rows[curr]; row != nil {
			if exp, ok := row[qualName]; ok {
				return exp, true
			}
			if exp, ok := row[fieldName]; ok {
				return exp, true
			}
		}
		if rootExceptions != nil {
			if exp, ok := rootExceptions[qualName]; ok {
				return exp, true
			}
			if exp, ok := rootExceptions[fieldName]; ok {
				return exp, true
			}
		}
		return "", false
	}
	var violations []string
	visited := make(map[string]bool)
	var scanNamed func(currName string, st *ast.StructType, owner *archPkgFile)
	var scanFields func(prefix string, st *ast.StructType, owner *archPkgFile)
	recurseNamed := func(ref string) {
		if visited[ref] {
			return
		}
		if target, ok := s.structs[ref]; ok {
			scanNamed(ref, target, s.ownerOf[ref])
			return
		}
		if p, n, ok := s.resolveArchLocal(ref); ok && p == "" {
			if target, ok := s.structs[n]; ok {
				scanNamed(n, target, s.ownerOf[n])
			}
		}
	}
	scanFields = func(prefix string, st *ast.StructType, owner *archPkgFile) {
		for _, field := range st.Fields.List {
			typeStr := aggregateFieldTypeToString(field.Type)
			isEmbedded := len(field.Names) == 0
			var namesToCheck []string
			if isEmbedded {
				derived := typeStr
				if star, ok := field.Type.(*ast.StarExpr); ok {
					derived = aggregateFieldTypeToString(star.X)
				}
				if sel, ok := field.Type.(*ast.SelectorExpr); ok {
					derived = sel.Sel.Name
				} else if star, ok := field.Type.(*ast.StarExpr); ok {
					if sel, ok := star.X.(*ast.SelectorExpr); ok {
						derived = sel.Sel.Name
					}
				}
				namesToCheck = append(namesToCheck, derived)
			} else {
				for _, n := range field.Names {
					namesToCheck = append(namesToCheck, n.Name)
				}
			}
			for _, fieldName := range namesToCheck {
				qualName := prefix + "." + fieldName
				allowed := false
				if exp, ok := lookupApproval(prefix, qualName, fieldName); ok {
					if !s.approvedExceptionMatches(field.Type, owner, exp) {
						violations = append(violations,
							fmt.Sprintf("%s (%s): field type does not match approved type %q",
								qualName, typeStr, exp))
						continue
					}
					allowed = true
				}
				label := qualName
				if isEmbedded {
					label = fmt.Sprintf("%s.[embedded %s]", prefix, typeStr)
				}
				if !allowed {
					if bad, found := s.forbiddenPkgInType(field.Type, owner); found {
						violations = append(violations,
							fmt.Sprintf("%s (%s): field type imports forbidden feature package %q",
								label, typeStr, bad))
						continue
					}
					lowerFieldName := strings.ToLower(fieldName)
					for _, tok := range archForbiddenFeatureTokens {
						if strings.Contains(lowerFieldName, tok) {
							violations = append(violations,
								fmt.Sprintf("%s: field name contains forbidden feature token %q",
									label, tok))
							break
						}
					}
					lowerTypeStr := strings.ToLower(typeStr)
					for _, tok := range archForbiddenFeatureTokens {
						if strings.Contains(lowerTypeStr, tok) {
							violations = append(violations,
								fmt.Sprintf("%s (%s): field type contains forbidden feature token %q",
									label, typeStr, tok))
							break
						}
					}
				}
				for _, ref := range namedArchRefs(field.Type) {
					recurseNamed(ref)
				}
				_, base := splitArchShape(field.Type)
				if inline, ok := base.(*ast.StructType); ok {
					scanFields(qualName, inline, owner)
				}
			}
		}
	}
	scanNamed = func(currName string, st *ast.StructType, owner *archPkgFile) {
		if visited[currName] {
			return
		}
		visited[currName] = true
		scanFields(currName, st, owner)
	}
	scanNamed(root, rootST, s.ownerOf[root])
	return violations
}
