package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestServe_PostHostFailureUsesHostCloseSeam proves access/auth and management
// startup failures delegate to closeServeHostAfterBuild / Host.Close and do not
// reconstruct Manager/Process ownership or call ShutdownTracing directly.
func TestServe_PostHostFailureUsesHostCloseSeam(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("command.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "command.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var closeCalls, rollbackCalls, processClose int
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "runServeCommand" || fd.Body == nil {
			return true
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				switch fun.Name {
				case "closeServeHostAfterBuild":
					closeCalls++
				case "serveHostRollback", "serveStartupRollback", "serveRollbackCore":
					rollbackCalls++
				case "closeServeProcessServices", "retireServeGenerations":
					processClose++
				}
			}
			return true
		})
		return false
	})
	if closeCalls < 2 {
		t.Fatalf("runServeCommand closeServeHostAfterBuild calls=%d want >=2 (access/auth + management)", closeCalls)
	}
	if rollbackCalls != 0 {
		t.Fatalf("runServeCommand must not use legacy rollback reconstruction; calls=%d", rollbackCalls)
	}
	if processClose != 0 {
		t.Fatalf("runServeCommand must not close process/generations directly; hits=%d", processClose)
	}
	text := string(src)
	if strings.Contains(text, "serveHostRollback") {
		t.Fatal("serveHostRollback must remain deleted from serve path")
	}
	if !strings.Contains(text, "tracingDeferred") {
		t.Fatal("serve must document temporary tracing defer boundary vs Host.Close")
	}
}

// TestCloseServeHostAfterBuild_SourceOwnsHostCloseOnly locks the helper to a
// single Host.Close seam without Manager/Process field extraction.
func TestCloseServeHostAfterBuild_SourceOwnsHostCloseOnly(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("serve_rollback.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, "func closeServeHostAfterBuild") {
		t.Fatal("missing closeServeHostAfterBuild")
	}
	if strings.Contains(text, "serveHostRollback") {
		t.Fatal("serveHostRollback must be removed")
	}
	// Extract only the closeServeHostAfterBuild function body for field checks.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "serve_rollback.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "closeServeHostAfterBuild" || fd.Body == nil {
			return true
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			switch sel.Sel.Name {
			case "Manager", "Process", "ShutdownTracing", "GenerationManager", "ProcessServices":
				t.Fatalf("closeServeHostAfterBuild must not extract Host field %s", sel.Sel.Name)
			}
			return true
		})
		return false
	})
}
