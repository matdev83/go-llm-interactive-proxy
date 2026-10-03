package archtest

// Shared AST primitives for the Agent-loop-guard ownership ratchets.
//
// These helpers carry no ALG policy. They only decompose a parsed source into the
// small facts the ratchets in agent_loop_guard_ownership_ratchet.go,
// agent_loop_guard_terminal_owner_census.go and
// agent_loop_guard_strategy_isolation_ratchet.go judge, so those files and
// their committed fixtures share one implementation.
//
// The overlay parsers at the end exist because the negative fixtures must run the
// SAME validators as the live repository scan. They mirror
// closed_plane_ratchet.go's scanClosedPlaneSyntheticSource.

import (
	"go/ast"
	"go/token"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// algCallHit is one observed call expression, decomposed just far enough to
// resolve it against a same-package declaration or an import alias.
//
// It carries no scope: the walk is strategy-parameterised rather than
// call-parameterised, and the receiver set is resolved once by the walk instead
// of per call (see ScanAgentLoopGuardStrategyReachability).
type algCallHit struct {
	node      ast.Node
	pkgIdent  string
	selName   string
	identName string
}

// matchesLocalName reports whether the call names a forbidden same-package
// entry point, either directly or as a selector field.
func (h algCallHit) matchesLocalName(name string) bool {
	return h.identName == name || h.selName == name
}

func algCallHits(stmts []ast.Stmt) []algCallHit {
	var out []algCallHit
	for _, stmt := range stmts {
		ast.Inspect(stmt, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			hit := algCallHit{node: call}
			switch fun := algCalleeExpr(call.Fun).(type) {
			case *ast.Ident:
				hit.identName = fun.Name
			case *ast.SelectorExpr:
				hit.selName = fun.Sel.Name
				if ident, ok := unwrapParen(fun.X).(*ast.Ident); ok {
					hit.pkgIdent = ident.Name
				}
			default:
				return true
			}
			out = append(out, hit)
			return true
		})
	}
	return out
}

// algCalleeExpr unwraps parentheses and generic instantiation so an instantiated
// call such as verifier.New[T]() resolves like its plain form.
func algCalleeExpr(fun ast.Expr) ast.Expr {
	expr := unwrapParen(fun)
	for {
		switch typed := expr.(type) {
		case *ast.IndexExpr:
			expr = unwrapParen(typed.X)
		case *ast.IndexListExpr:
			expr = unwrapParen(typed.X)
		default:
			return expr
		}
	}
}

// algAppendAssignment is a direct append into a canonical client/A-leg field,
// written either through the field selector itself or through a local alias
// bound to it.
type algAppendAssignment struct {
	field string
	node  ast.Node
}

// algAppendAssignments reports every direct append into a canonical
// client/A-leg field, in the direct, additive, and ALIASED shapes:
//
//	call.Messages = append(call.Messages, msg)   // direct append
//	call.Messages += append(call.Messages, msg)  // additive spelling
//	msgs := call.Messages
//	msgs = append(msgs, msg)                     // aliased append
//
// Aliases are resolved per function body: a local name bound by := or var to a
// forbidden field selector is treated as that field for the rest of that body.
// The analysis is flow-insensitive on purpose. A ratchet may report a superset
// of a flow-sensitive fact, never a subset.
func algAppendAssignments(f *ast.File) []algAppendAssignment {
	var out []algAppendAssignment
	for _, body := range algFunctionBodies(f) {
		aliases := algForbiddenFieldAliases(body)
		ast.Inspect(body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			switch assign.Tok {
			case token.ASSIGN, token.DEFINE:
				if !algContainsAppendCall(assign.Rhs) {
					return true
				}
			case token.ADD_ASSIGN:
				// `s += append(s, v)` is the additive spelling of the same
				// mutation on a slice. Any other += right-hand side is not an
				// append, so it is not this ratchet's business.
				if !algContainsAppendCall(assign.Rhs) {
					return true
				}
			default:
				return true
			}
			for _, lhs := range assign.Lhs {
				switch typed := unwrapParen(lhs).(type) {
				case *ast.SelectorExpr:
					if algForbiddenClientAppendFields[typed.Sel.Name] {
						out = append(out, algAppendAssignment{field: typed.Sel.Name, node: assign})
					}
				case *ast.Ident:
					if field, aliased := aliases[typed.Name]; aliased {
						out = append(out, algAppendAssignment{field: field, node: assign})
					}
				}
			}
			return true
		})
	}
	return out
}

// algFunctionBodies returns every function body in the file, including function
// literals, so a violation inside a closure is covered by the same analysis.
func algFunctionBodies(f *ast.File) []ast.Node {
	var out []ast.Node
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		out = append(out, fn.Body)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if lit, ok := n.(*ast.FuncLit); ok {
			out = append(out, lit.Body)
		}
		return true
	})
	return out
}

// algForbiddenFieldAliases maps a local name bound to a canonical client/A-leg
// field onto that field name.
func algForbiddenFieldAliases(body ast.Node) map[string]string {
	aliases := make(map[string]string)
	ast.Inspect(body, func(n ast.Node) bool {
		switch typed := n.(type) {
		case *ast.AssignStmt:
			for i, rhs := range typed.Rhs {
				if i >= len(typed.Lhs) {
					break
				}
				field := algForbiddenFieldOf(rhs)
				if field == "" {
					continue
				}
				if name, ok := unwrapParen(typed.Lhs[i]).(*ast.Ident); ok {
					aliases[name.Name] = field
				}
			}
		case *ast.DeclStmt:
			decl, ok := typed.Decl.(*ast.GenDecl)
			if !ok || decl.Tok != token.VAR {
				return true
			}
			for _, spec := range decl.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != len(value.Values) {
					continue
				}
				field := algForbiddenFieldOf(value.Values[0])
				if field == "" {
					continue
				}
				aliases[value.Names[0].Name] = field
			}
		}
		return true
	})
	return aliases
}

// algForbiddenFieldOf returns the canonical client/A-leg field name an
// expression reads, or "" when it reads no forbidden field.
func algForbiddenFieldOf(expr ast.Expr) string {
	sel, ok := unwrapParen(expr).(*ast.SelectorExpr)
	if !ok || !algForbiddenClientAppendFields[sel.Sel.Name] {
		return ""
	}
	return sel.Sel.Name
}

func algContainsAppendCall(exprs []ast.Expr) bool {
	found := false
	for _, expr := range exprs {
		ast.Inspect(expr, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := algCalleeExpr(call.Fun).(*ast.Ident); ok && ident.Name == "append" {
				found = true
				return false
			}
			return true
		})
	}
	return found
}

type algIdentHit struct {
	name string
	node ast.Node
}

func algIdentifierHits(f *ast.File, forbidden map[string]string) []algIdentHit {
	var out []algIdentHit
	ast.Inspect(f, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if _, hit := forbidden[ident.Name]; hit {
			out = append(out, algIdentHit{name: ident.Name, node: ident})
		}
		return true
	})
	return out
}

type algLiteralHit struct {
	text string
	node ast.Node
}

// algStringLiteralHits reports the forbidden tokens embedded in any unquoted
// string literal, so both a bare comparison and a longer diagnostic message are
// refused.
func algStringLiteralHits(f *ast.File, forbidden map[string]string) []algLiteralHit {
	var out []algLiteralHit
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		for forbiddenToken := range forbidden {
			if strings.Contains(value, forbiddenToken) {
				out = append(out, algLiteralHit{text: forbiddenToken, node: lit})
			}
		}
		return true
	})
	return out
}

// algImportAliases maps the local name of each named import to its full path.
func algImportAliases(f *ast.File) map[string]string {
	out := make(map[string]string)
	if f == nil {
		return out
	}
	for _, spec := range f.Imports {
		if spec.Path == nil {
			continue
		}
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := algPathBase(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name == "_" || name == "." || name == "" {
			continue
		}
		out[name] = path
	}
	return out
}

// algForbiddenImport reports whether path ends with one of the forbidden
// suffixes. Suffix matching keeps the ratchet valid for a vendored module that
// reproduces the same import path.
func algForbiddenImport(path string, suffixes []string) bool {
	for _, suffix := range suffixes {
		if path == suffix || strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

func algPathBase(path string) string {
	base := path
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		base = path[idx+1:]
	}
	if idx := strings.LastIndex(base, "."); idx > 0 {
		base = base[:idx]
	}
	return base
}

// algIsControlToolProviderType reports whether expr is the SDK control-tool
// provider type. The import qualifier is checked literally because the
// generated view is in package feature and the alias is fixed by the generator.
func algIsControlToolProviderType(expr ast.Expr) bool {
	sel, ok := unwrapParen(expr).(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := unwrapParen(sel.X).(*ast.Ident)
	return ok && ident.Name == "controltool" && sel.Sel.Name == "Provider"
}

func algReceiverBaseName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	return algTypeName(recv.List[0].Type)
}

func algTypeName(expr ast.Expr) string {
	switch typed := unwrapParen(expr).(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.SelectorExpr:
		if qual, ok := unwrapParen(typed.X).(*ast.Ident); ok {
			return qual.Name + "." + typed.Sel.Name
		}
	case *ast.StarExpr:
		return algTypeName(typed.X)
	case *ast.IndexExpr:
		return algTypeName(typed.X)
	case *ast.IndexListExpr:
		return algTypeName(typed.X)
	}
	return ""
}

func algLine(fset *token.FileSet, node ast.Node) int {
	if fset == nil || node == nil {
		return 0
	}
	return fset.Position(node.Pos()).Line
}

// scanAlgOwnershipSyntheticSource parses a miniature violating source and runs
// the per-file ratchet over it. It exists so a fixture asserts the verdict of
// the same validator the repository walk runs.
func scanAlgOwnershipSyntheticSource(t *testing.T, relPath, src string) []RuleFinding {
	t.Helper()
	fset, f, err := ParseGoSource(relPath, []byte(src))
	require.NoError(t, err)
	return ScanFileAgentLoopGuardOwnership(relPath, fset, f)
}

// parseAlgOverlayFiles parses a miniature same-package overlay keyed by
// repo-relative path.
func parseAlgOverlayFiles(t *testing.T, sources map[string]string) []algSourceFile {
	t.Helper()
	paths := sortedKeys(sources)
	files := make([]algSourceFile, 0, len(paths))
	for _, rel := range paths {
		fset, f, err := ParseGoSource(rel, []byte(sources[rel]))
		require.NoError(t, err, "parse overlay file %s", rel)
		files = append(files, algSourceFile{RelPath: rel, AST: f, FSet: fset})
	}
	return files
}

func sortedKeys[V any](in map[string]V) []string {
	out := make([]string, 0, len(in))
	for key := range in {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func algSortedUnique(in []string) []string {
	sorted := append([]string(nil), in...)
	sort.Strings(sorted)
	out := sorted[:0]
	for i, v := range sorted {
		if i == 0 || v != sorted[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// appendUnique appends value to in unless in already carries it, so repeated
// observations of one declaration do not inflate a census.
func appendUnique(in []string, value string) []string {
	if value == "" || slices.Contains(in, value) {
		return in
	}
	return append(in, value)
}

func sortedImportPaths(in map[string]string) []string {
	out := make([]string, 0, len(in))
	for _, path := range in {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// algAllowedFeatureImports returns the complete permitted import closure of the
// ALG feature root package, sorted for stable diagnostics. Standard library
// imports are always allowed, so only non-stdlib entries are listed.
func algAllowedFeatureImports() []string {
	return sortedKeys(algFeatureImportAllowlist)
}

// algFeatureImportAllowed reports whether path may be imported anywhere in the
// ALG feature: its own subpackages, the canonical and SDK contracts it is
// designed against, and the standard library.
//
// The standard-library test is the Go toolchain's own reservation rule: the first
// element of an import path MUST contain a dot unless it names a standard
// library package, so a dotless first element cannot name a fetchable module. That
// makes the test sound for the question this ratchet asks - could this import
// reach a second policy owner? - instead of a list that could rot.
func algFeatureImportAllowed(path string, allowed []string) bool {
	for _, permitted := range allowed {
		if path == permitted {
			return true
		}
	}
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}
