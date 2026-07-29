package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestFrontendServeHTTPPreflightBeforeDecode locks shared create-pipeline call order by
// source position (not CFG dominance): reqbody.ReadAll -> jsonguard.PreflightContext ->
// decodeqos.TryAdmit -> (optional FromModelOrDefault) -> decode (via spec.Decode inside
// decodeqos.Guard). Frontends delegate ServeHTTP to frontendpipe.ServeHTTP.
func TestFrontendServeHTTPPreflightBeforeDecode(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "plugins", "frontends", "frontendpipe", "pipe.go")
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
		t.Fatalf("frontendpipe: ServeHTTP not found")
	}
	var readAllPos, preflightPos, tryAdmitPos, routeExtractPos, decodeGuardPos token.Pos
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
		case pkg == "jsonguard" && cname == "PreflightContext":
			if preflightPos == 0 {
				preflightPos = call.Pos()
			}
		case pkg == "decodeqos" && cname == "TryAdmit":
			if tryAdmitPos == 0 {
				tryAdmitPos = call.Pos()
			}
		case cname == "FromModelOrDefault":
			if routeExtractPos == 0 {
				routeExtractPos = call.Pos()
			}
		case pkg == "decodeqos" && cname == "Guard":
			if decodeGuardPos == 0 {
				decodeGuardPos = call.Pos()
			}
		}
		return true
	})
	if readAllPos == 0 || preflightPos == 0 || tryAdmitPos == 0 || decodeGuardPos == 0 {
		t.Fatalf("frontendpipe ServeHTTP: missing ReadAll(%v) PreflightContext(%v) TryAdmit(%v) decodeqos.Guard(%v)",
			readAllPos != 0, preflightPos != 0, tryAdmitPos != 0, decodeGuardPos != 0)
	}
	if readAllPos >= preflightPos || preflightPos >= tryAdmitPos || tryAdmitPos >= decodeGuardPos {
		t.Fatalf("frontendpipe ServeHTTP: want ReadAll < PreflightContext < TryAdmit < decodeqos.Guard; positions %d %d %d %d",
			readAllPos, preflightPos, tryAdmitPos, decodeGuardPos)
	}
	if routeExtractPos != 0 && routeExtractPos <= tryAdmitPos {
		t.Fatalf("frontendpipe ServeHTTP: want TryAdmit < FromModelOrDefault; positions admit=%d route=%d",
			tryAdmitPos, routeExtractPos)
	}
}

// TestToolCallRepairMaterializeAfterPreflight requires parseOrderedJSON /
// unmarshalSchemaJSON call sites (outside exempt helpers) to be preceded in the
// same function by preflightArgsJSON or preflightSchemaJSON (source order only).
// repairPreflightedArgsJSON is exempt: engine calls it only after args preflight
// and schema cache compile. materializeFillValue re-parses trusted in-memory fills.
func TestToolCallRepairMaterializeAfterPreflight(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "core", "toolcallrepair")
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name == nil {
				continue
			}
			fname := fn.Name.Name
			switch fname {
			case "parseOrderedJSON", "unmarshalSchemaJSON", "materializeFillValue", "repairPreflightedArgsJSON":
				continue
			}
			var preflightPos token.Pos
			var materializeCalls []token.Pos
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				_, cname := qualifiedCall(call.Fun)
				switch cname {
				case "preflightArgsJSON", "preflightSchemaJSON":
					if preflightPos == 0 {
						preflightPos = call.Pos()
					}
				case "parseOrderedJSON", "unmarshalSchemaJSON":
					materializeCalls = append(materializeCalls, call.Pos())
				}
				return true
			})
			for _, mp := range materializeCalls {
				if preflightPos == 0 || preflightPos >= mp {
					t.Fatalf("%s:%s: must call preflightArgsJSON/preflightSchemaJSON before materialize",
						filepath.Base(path), fname)
				}
			}
		}
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
