package archtest

import (
	"go/ast"
	"strings"
	"testing"
)

// Alias/defined-container resolution for the generic-aggregates ratchet.
//
// R3a: forbiddenPkgInType must see through alias AND defined container
// declarations (map/array/chan/func/interface shapes) instead of stopping at
// the declaration boundary. R3b: exception matching must compare the complete
// resolved shape (outer field shape composed with alias-inner shapes) and must
// treat defined types as opaque identities distinct from what they wrap.

// forbiddenInDeclType reports whether the declaration of local type name
// references a forbidden feature package, walking into container shapes and
// following nested local declarations. visiting guards alias cycles.
func (s *archPkgScope) forbiddenInDeclType(name string, visiting map[string]bool) (string, bool) {
	if visiting[name] {
		return "", false
	}
	under, declared := s.declared[name]
	if !declared {
		return "", false
	}
	visiting[name] = true
	return s.forbiddenInDeclExpr(under, s.declFile[name], visiting)
}

// forbiddenInDeclExpr scans one declaration expression for forbidden package
// references using the declaration owner's import map. Nested local names are
// followed through both aliases and defined types with cycle protection.
func (s *archPkgScope) forbiddenInDeclExpr(e ast.Expr, file *archPkgFile, visiting map[string]bool) (string, bool) {
	switch t := e.(type) {
	case *ast.SelectorExpr:
		if x, ok := t.X.(*ast.Ident); ok {
			if p, ok := file.imports[x.Name]; ok && archForbiddenPkg(p) {
				return p, true
			}
		}
		return "", false
	case *ast.Ident:
		if p, _, ok := s.resolveArchLocal(t.Name); ok && p != "" && archForbiddenPkg(p) {
			return p, true
		}
		if _, declared := s.declared[t.Name]; declared {
			return s.forbiddenInDeclType(t.Name, visiting)
		}
		return "", false
	case *ast.StarExpr:
		return s.forbiddenInDeclExpr(t.X, file, visiting)
	case *ast.ArrayType:
		return s.forbiddenInDeclExpr(t.Elt, file, visiting)
	case *ast.MapType:
		if bad, hit := s.forbiddenInDeclExpr(t.Key, file, visiting); hit {
			return bad, true
		}
		return s.forbiddenInDeclExpr(t.Value, file, visiting)
	case *ast.ChanType:
		return s.forbiddenInDeclExpr(t.Value, file, visiting)
	case *ast.Ellipsis:
		return s.forbiddenInDeclExpr(t.Elt, file, visiting)
	case *ast.ParenExpr:
		return s.forbiddenInDeclExpr(t.X, file, visiting)
	case *ast.StructType:
		if t.Fields != nil {
			for _, f := range t.Fields.List {
				if bad, hit := s.forbiddenInDeclExpr(f.Type, file, visiting); hit {
					return bad, true
				}
			}
		}
		return "", false
	case *ast.FuncType:
		if t.Params != nil {
			for _, f := range t.Params.List {
				if bad, hit := s.forbiddenInDeclExpr(f.Type, file, visiting); hit {
					return bad, true
				}
			}
		}
		if t.Results != nil {
			for _, f := range t.Results.List {
				if bad, hit := s.forbiddenInDeclExpr(f.Type, file, visiting); hit {
					return bad, true
				}
			}
		}
		return "", false
	case *ast.InterfaceType:
		if t.Methods != nil {
			for _, f := range t.Methods.List {
				if bad, hit := s.forbiddenInDeclExpr(f.Type, file, visiting); hit {
					return bad, true
				}
			}
		}
		return "", false
	default:
		return "", false
	}
}

// resolveArchFullType resolves a field type to its complete identity: the
// composed shape plus the terminal package path and type name. Alias
// declarations are transparent (their inner shapes compose with the outer
// field shape); defined types are opaque (traversal stops and reports the
// defined name itself). Selectors resolve through the owning file's imports.
func (s *archPkgScope) resolveArchFullType(expr ast.Expr, file *archPkgFile) (shape, importPath, typeName string, ok bool) {
	shape, base := splitArchShape(expr)
	switch b := base.(type) {
	case *ast.SelectorExpr:
		x, ok := b.X.(*ast.Ident)
		if !ok {
			return "", "", "", false
		}
		p, ok := file.imports[x.Name]
		if !ok {
			return "", "", "", false
		}
		return shape, p, b.Sel.Name, true
	case *ast.Ident:
		return s.resolveArchFullName(b.Name, shape, make(map[string]bool))
	default:
		return "", "", "", false
	}
}

// resolveArchFullName walks one local name through alias declarations,
// composing shapes. A struct or defined type ends the walk as a same-package
// identity; an alias to another package ends it as a qualified identity.
func (s *archPkgScope) resolveArchFullName(name, shape string, visiting map[string]bool) (string, string, string, bool) {
	curr := name
	for {
		if visiting[curr] {
			return "", "", "", false
		}
		visiting[curr] = true
		if _, isStruct := s.structs[curr]; isStruct {
			return shape, "", curr, true
		}
		under, declared := s.declared[curr]
		if !declared {
			return "", "", "", false
		}
		if !s.aliasOf[curr] {
			return shape, "", curr, true
		}
		innerShape, innerBase := splitArchShape(under)
		shape += innerShape
		switch b := innerBase.(type) {
		case *ast.SelectorExpr:
			x, ok := b.X.(*ast.Ident)
			if !ok {
				return "", "", "", false
			}
			p, ok := s.declFile[curr].imports[x.Name]
			if !ok {
				return "", "", "", false
			}
			return shape, p, b.Sel.Name, true
		case *ast.Ident:
			curr = b.Name
		default:
			return "", "", "", false
		}
	}
}

// TestGenericAggregates_R3aVariadicParenForms closes the Ellipsis/ParenExpr
// bypass: variadic func aliases/chains and paren-wrapped map values hiding a
// forbidden package must be flagged, in both alias and defined-type forms.
// Neutral variadic/paren shapes with no forbidden package stay silent.
func TestGenericAggregates_R3aVariadicParenForms(t *testing.T) {
	t.Parallel()
	keepwarmImport := `"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/keepwarm"`
	positive := []struct {
		name    string
		sources map[string]string
		target  string
	}{
		{
			name: "alias func variadic chain",
			sources: map[string]string{"synthetic.go": `package fixture
import ` + keepwarmImport + `
type Callback = func(...*keepwarm.Manager)
type CallbackChain = Callback
type VariadicAliasAggregate struct {
	Handler CallbackChain
}
`},
			target: "VariadicAliasAggregate",
		},
		{
			name: "defined func variadic chain",
			sources: map[string]string{"synthetic.go": `package fixture
import ` + keepwarmImport + `
type CallbackDef func(...*keepwarm.Manager)
type CallbackDefChain CallbackDef
type VariadicDefinedAggregate struct {
	Handler CallbackDefChain
}
`},
			target: "VariadicDefinedAggregate",
		},
		{
			name: "alias paren-wrapped map value chain",
			sources: map[string]string{"synthetic.go": `package fixture
import ` + keepwarmImport + `
type ManagerBagParen = map[string](*keepwarm.Manager)
type ManagerBagParenChain = ManagerBagParen
type ParenAliasAggregate struct {
	Managers ManagerBagParenChain
}
`},
			target: "ParenAliasAggregate",
		},
		{
			name: "defined paren-wrapped map value chain",
			sources: map[string]string{"synthetic.go": `package fixture
import ` + keepwarmImport + `
type ManagerBagParenDef map[string](*keepwarm.Manager)
type ManagerBagParenDefChain ManagerBagParenDef
type ParenDefinedAggregate struct {
	Managers ManagerBagParenDefChain
}
`},
			target: "ParenDefinedAggregate",
		},
	}
	for _, tc := range positive {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope := archParseSources(t, tc.sources)
			got := scope.scan(tc.target, nil)
			if len(got) == 0 {
				t.Fatalf("expected at least 1 violation for %s, got zero", tc.target)
			}
		})
	}

	neutral := []struct {
		name    string
		sources map[string]string
		target  string
	}{
		{
			name: "neutral variadic func",
			sources: map[string]string{"synthetic.go": `package fixture
type NeutralCallback = func(...int)
type NeutralCallbackChain = NeutralCallback
type NeutralVariadicAggregate struct {
	Handler NeutralCallbackChain
}
`},
			target: "NeutralVariadicAggregate",
		},
		{
			name: "neutral paren-wrapped types",
			sources: map[string]string{"synthetic.go": `package fixture
type NeutralBag = map[string](*int)
type NeutralBagChain = NeutralBag
type NeutralParenAggregate struct {
	Managers NeutralBagChain
	Single (int)
}
`},
			target: "NeutralParenAggregate",
		},
	}
	for _, tc := range neutral {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope := archParseSources(t, tc.sources)
			got := scope.scan(tc.target, nil)
			if len(got) != 0 {
				t.Fatalf("expected zero violations, got %d:\n%s", len(got), strings.Join(got, "\n"))
			}
		})
	}
}
