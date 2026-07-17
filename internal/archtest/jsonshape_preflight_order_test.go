package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestFrontendServeHTTPPreflightBeforeDecode locks ServeHTTP call order by
// source position (not CFG dominance): reqbody.ReadAll → jsonguard.Preflight →
// Decode*. Behavioral depth/gzip/UTF-8 tests prove the runtime gate.
func TestFrontendServeHTTPPreflightBeforeDecode(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	frontends := []string{"openairesponses", "openailegacy", "anthropic", "gemini"}
	for _, name := range frontends {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(root, "internal", "plugins", "frontends", name, "handler.go")
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			var serve *ast.FuncDecl
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name == nil || fn.Name.Name != "ServeHTTP" || fn.Body == nil {
					continue
				}
				serve = fn
				break
			}
			if serve == nil {
				t.Fatalf("%s: ServeHTTP not found", name)
			}
			var readAllPos, preflightPos, decodePos token.Pos
			ast.Inspect(serve.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				pkg, cname := qualifiedCall(call.Fun)
				switch {
				case pkg == "reqbody" && cname == "ReadAll":
					if readAllPos == 0 {
						readAllPos = call.Pos()
					}
				case pkg == "jsonguard" && cname == "Preflight":
					if preflightPos == 0 {
						preflightPos = call.Pos()
					}
				case strings.HasPrefix(cname, "Decode") && pkg == "":
					if decodePos == 0 {
						decodePos = call.Pos()
					}
				}
				return true
			})
			if readAllPos == 0 || preflightPos == 0 || decodePos == 0 {
				t.Fatalf("%s ServeHTTP: missing ReadAll(%v) Preflight(%v) Decode*(%v)",
					name, readAllPos != 0, preflightPos != 0, decodePos != 0)
			}
			if !(readAllPos < preflightPos && preflightPos < decodePos) {
				t.Fatalf("%s ServeHTTP: want ReadAll < Preflight < Decode*; positions %d %d %d",
					name, readAllPos, preflightPos, decodePos)
			}
		})
	}
}

func qualifiedCall(fun ast.Expr) (pkg, name string) {
	switch f := fun.(type) {
	case *ast.Ident:
		return "", f.Name
	case *ast.SelectorExpr:
		name = ""
		if f.Sel != nil {
			name = f.Sel.Name
		}
		if id, ok := f.X.(*ast.Ident); ok {
			return id.Name, name
		}
		return "", name
	}
	return "", ""
}
