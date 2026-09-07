package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// scanFeaturehostReflectViolations reports reflective construction or method
// dispatch in one featurehost source file. Production reflection is limited
// to startup typed-nil capability guards: `reflect.ValueOf` plus
// `Kind`/`IsNil` inspection inside isNil/IsNil-named functions. The reflect
// package is resolved under any local import name, so aliased imports cannot
// escape. Reflective use anywhere else — including package-level
// initializers and dynamic dispatch (MethodByName/Call/Set/Interface-style)
// even inside nil-guard-named functions — is a violation.
func scanFeaturehostReflectViolations(rel string, src []byte) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return nil, err
	}
	rel = SlashPath(rel)
	reflectNames, dotImport := reflectLocalNames(f)
	if dotImport {
		return []string{rel + ": reflect dot-imported into featurehost scope"}, nil
	}
	s := &reflectGuardScanner{rel: rel, reflectNames: reflectNames}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name == nil {
				continue
			}
			s.scanFunc(d)
		default:
			s.inNilGuard = false
			s.valueVars = map[string]bool{}
			s.collectValueVars(d)
			ast.Inspect(d, s.visit)
		}
	}
	return s.violations, nil
}

type reflectGuardScanner struct {
	rel          string
	reflectNames map[string]bool
	inNilGuard   bool
	valueVars    map[string]bool
	violations   []string
}

func (s *reflectGuardScanner) scanFunc(fn *ast.FuncDecl) {
	s.inNilGuard = strings.HasPrefix(fn.Name.Name, "isNil") || strings.HasPrefix(fn.Name.Name, "IsNil")
	s.valueVars = map[string]bool{}
	s.collectValueVars(fn)
	ast.Inspect(fn, s.visit)
}

// collectValueVars records identifiers bound to a reflect.ValueOf result so
// later method dispatch on them (MethodByName/Call/Set/...) is recognized
// even though the receiver is not the reflect package itself.
func (s *reflectGuardScanner) collectValueVars(n ast.Node) {
	ast.Inspect(n, func(node ast.Node) bool {
		switch v := node.(type) {
		case *ast.AssignStmt:
			for _, rhs := range v.Rhs {
				if isValueOfCall(rhs, s.reflectNames) {
					for _, lhs := range v.Lhs {
						if id, ok := lhs.(*ast.Ident); ok {
							s.valueVars[id.Name] = true
						}
					}
				}
			}
		case *ast.ValueSpec:
			for _, val := range v.Values {
				if isValueOfCall(val, s.reflectNames) {
					for _, name := range v.Names {
						s.valueVars[name.Name] = true
					}
				}
			}
		}
		return true
	})
}

func (s *reflectGuardScanner) visit(n ast.Node) bool {
	sel, ok := n.(*ast.SelectorExpr)
	if !ok {
		return true
	}
	if id, ok := unparenExpr(sel.X).(*ast.Ident); ok && s.reflectNames[id.Name] {
		if !s.inNilGuard || !reflectPackageSelectorAllowed(sel.Sel.Name) {
			s.violations = append(s.violations, s.rel+": reflect outside typed-nil guard")
		}
		return true
	}
	if s.isReflectValue(sel.X) && sel.Sel.Name != "Kind" && sel.Sel.Name != "IsNil" {
		s.violations = append(s.violations, s.rel+": reflect dynamic dispatch outside typed-nil guard")
		return true
	}
	if !s.inNilGuard && s.isReflectValue(sel.X) {
		s.violations = append(s.violations, s.rel+": reflect outside typed-nil guard")
	}
	return true
}

// isReflectValue reports whether an expression is a reflect.Value: either a
// tracked ValueOf-bound identifier or a call/selector chain rooted at
// reflect.ValueOf (e.g. reflect.ValueOf(x).MethodByName(name)).
func (s *reflectGuardScanner) isReflectValue(expr ast.Expr) bool {
	switch e := unparenExpr(expr).(type) {
	case *ast.Ident:
		return s.valueVars[e.Name]
	case *ast.CallExpr:
		return s.isReflectValue(e.Fun)
	case *ast.SelectorExpr:
		if id, ok := unparenExpr(e.X).(*ast.Ident); ok && s.reflectNames[id.Name] && e.Sel.Name == "ValueOf" {
			return true
		}
		return s.isReflectValue(e.X)
	default:
		return false
	}
}

func isValueOfCall(expr ast.Expr, reflectNames map[string]bool) bool {
	call, ok := unparenExpr(expr).(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := unparenExpr(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := unparenExpr(sel.X).(*ast.Ident)
	return ok && reflectNames[id.Name] && sel.Sel.Name == "ValueOf"
}

// reflectLocalNames maps every local identifier bound to the reflect package
// in this file, so aliased imports (r "reflect") cannot escape the ratchet.
// It also reports dot-imports, which would hide reflective calls as bare
// identifiers. Blank imports bind nothing observable.
func reflectLocalNames(f *ast.File) (map[string]bool, bool) {
	out := map[string]bool{}
	dot := false
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if path != "reflect" {
			continue
		}
		if imp.Name != nil {
			switch imp.Name.Name {
			case "_":
			case ".":
				dot = true
			default:
				out[imp.Name.Name] = true
			}
			continue
		}
		out["reflect"] = true
	}
	return out, dot
}

// reflectPackageSelectorAllowed allowlists the typed-nil guard vocabulary on
// the reflect package itself: ValueOf construction, Value/Type/Kind type
// references, and reflect.Kind constant comparisons. Everything else
// (TypeOf/New/Zero/MakeFunc/...) is reflective surface that needs review.
func reflectPackageSelectorAllowed(sel string) bool {
	switch sel {
	case "ValueOf", "Value", "Type", "Kind":
		return true
	}
	return reflectKindConstant(sel)
}

func reflectKindConstant(sel string) bool {
	switch sel {
	case "Invalid", "Bool",
		"Int", "Int8", "Int16", "Int32", "Int64",
		"Uint", "Uint8", "Uint16", "Uint32", "Uint64", "Uintptr",
		"Float32", "Float64", "Complex64", "Complex128",
		"Array", "Chan", "Func", "Interface", "Map", "Pointer",
		"Slice", "String", "Struct", "UnsafePointer":
		return true
	default:
		return false
	}
}

func unparenExpr(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}
