package archtest

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStopguardImportsAllowlist enforces the pure policy boundary of
// internal/core/stopguard (requirements 2.3, 10.3; task 10.2).
// Stopguard must ONLY import Go standard library packages and pkg/lipapi,
// with zero dependencies on provider SDKs, runtime, auxreq, verify adapters,
// plugins, infra, or I/O packages.
func TestStopguardImportsAllowlist(t *testing.T) {
	t.Parallel()

	out, err := cachedGoList(t, "-json", "-test=false", "./internal/core/stopguard")
	if err != nil {
		t.Fatalf("go list ./internal/core/stopguard: %v", err)
	}

	dec := json.NewDecoder(bytes.NewReader(out))
	var pkg goListPackage
	if err := dec.Decode(&pkg); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, imp := range pkg.Imports {
		// Disallow forbidden stdlib packages (I/O, OS, network)
		if imp == "os" || strings.HasPrefix(imp, "os/") ||
			imp == "net" || strings.HasPrefix(imp, "net/") ||
			imp == "database/sql" || strings.HasPrefix(imp, "database/sql/") {
			t.Fatalf("internal/core/stopguard imports forbidden I/O package %q (must be pure policy)", imp)
		}
		// If it is a non-stdlib package (contains domain dot), must be pkg/lipapi
		if strings.Contains(imp, ".") {
			if !strings.HasPrefix(imp, "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi") {
				t.Fatalf("internal/core/stopguard imports non-allowlisted package %q (allowed: stdlib, pkg/lipapi)", imp)
			}
		}
	}

	// Also verify transitive dependencies exclude all forbidden domains.
	forbiddenTransitive := []forbiddenDep{
		{Substr: "/internal/core/runtime", ErrMsg: "stopguard must not depend on core runtime"},
		{Substr: "/internal/core/stopguardverify", ErrMsg: "stopguard must not depend on stopguardverify adapter (verifier must stay outside)"},
		{Substr: "/internal/core/stopgate", ErrMsg: "stopguard must not depend on stopgate"},
		{Substr: "/internal/core/auxreq", ErrMsg: "stopguard must not depend on auxreq (no auxiliary I/O)"},
		{Substr: "/internal/infra", ErrMsg: "stopguard must not depend on internal/infra"},
		{Substr: "/internal/plugins", ErrMsg: "stopguard must not depend on concrete plugins"},
		{Substr: "/internal/stdhttp", ErrMsg: "stopguard must not depend on stdhttp"},
		{Substr: "/internal/pluginreg", ErrMsg: "stopguard must not depend on pluginreg"},
		{Substr: "github.com/openai/openai-go", ErrMsg: "stopguard must not depend on OpenAI SDK"},
		{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "stopguard must not depend on Anthropic SDK"},
		{Substr: "google.golang.org/genai", ErrMsg: "stopguard must not depend on Gemini SDK"},
		{Substr: "github.com/aws/aws-sdk-go-v2", ErrMsg: "stopguard must not depend on AWS SDK"},
		{Substr: "database/sql", ErrMsg: "stopguard must not depend on database/sql"},
		{Substr: "uptrace/bun", ErrMsg: "stopguard must not depend on Bun"},
		{Substr: "net/http", ErrMsg: "stopguard must not depend on net/http (pure policy no I/O)"},
		{Substr: "net", ErrMsg: "stopguard must not depend on net (pure policy no I/O)"},
		{Substr: "os", ErrMsg: "stopguard must not depend on os (pure policy no I/O)"},
	}
	assertDepsExcludeForbidden(t, []string{"./internal/core/stopguard"}, forbiddenTransitive)
}

// TestStopguardPackagePurity verifies via AST that internal/core/stopguard
// contains only pure functions, types, and constants, with no package-level mutable
// state, no init() functions, no goroutines, no channel operations, and no I/O calls.
func TestStopguardPackagePurity(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "core", "stopguard")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read stopguard dir: %v", err)
	}

	fset := token.NewFileSet()
	for _, ent := range entries {
		if ent.IsDir() || strings.HasSuffix(ent.Name(), "_test.go") || !strings.HasSuffix(ent.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
		if err != nil {
			t.Fatalf("parse %s: %v", ent.Name(), err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				if node.Name.Name == "init" {
					pos := fset.Position(node.Pos())
					t.Fatalf("%s:%d: stopguard must not declare init() function", ent.Name(), pos.Line)
				}
			case *ast.GenDecl:
				if node.Tok == token.VAR {
					for _, spec := range node.Specs {
						vspec, ok := spec.(*ast.ValueSpec)
						if ok {
							for _, val := range vspec.Values {
								// Disallow mutable global variables (maps, slices, channels, pointers)
								switch val.(type) {
								case *ast.CompositeLit, *ast.CallExpr:
									pos := fset.Position(val.Pos())
									t.Fatalf("%s:%d: stopguard must not declare mutable package-level var %v", ent.Name(), pos.Line, vspec.Names)
								}
							}
						}
					}
				}
			case *ast.GoStmt:
				pos := fset.Position(node.Pos())
				t.Fatalf("%s:%d: stopguard must not spawn goroutines (must be pure synchronous policy)", ent.Name(), pos.Line)
			case *ast.ChanType:
				pos := fset.Position(node.Pos())
				t.Fatalf("%s:%d: stopguard must not use channels (pure policy, no async coordination)", ent.Name(), pos.Line)
			case *ast.SendStmt:
				pos := fset.Position(node.Pos())
				t.Fatalf("%s:%d: stopguard must not perform channel operations", ent.Name(), pos.Line)
			case *ast.UnaryExpr:
				if node.Op == token.ARROW {
					pos := fset.Position(node.Pos())
					t.Fatalf("%s:%d: stopguard must not receive from channels", ent.Name(), pos.Line)
				}
			}
			return true
		})
	}
}

// TestStopguardDocGoPresenceAndContent verifies that internal/core/stopguard/doc.go
// exists and documents the pure policy boundary and no-I/O guarantee.
func TestStopguardDocGoPresenceAndContent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	docPath := filepath.Join(root, "internal", "core", "stopguard", "doc.go")
	content, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read stopguard/doc.go: %v", err)
	}

	text := string(content)
	if !strings.Contains(text, "package stopguard") {
		t.Fatalf("doc.go missing package stopguard declaration")
	}
	if !strings.Contains(text, "pure") || !strings.Contains(text, "no I/O") {
		t.Fatalf("doc.go must explicitly document pure policy and no I/O boundary, got:\n%s", text)
	}
}

// TestContinuationCallGraphExcludesRetryReplacement verifies that post-output
// and semantic continuation call paths in internal/core/runtime do not use
// retry or replacement semantics (requirements 4.1, 9.5; task 10.2).
func TestContinuationCallGraphExcludesRetryReplacement(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	runtimeDir := filepath.Join(root, "internal", "core", "runtime")

	fset := token.NewFileSet()
	contFile := filepath.Join(runtimeDir, "agent_loop_guard_continuation.go")
	f, err := parser.ParseFile(fset, contFile, nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse agent_loop_guard_continuation.go: %v", err)
	}

	foundReplacementOpenReq := false
	ast.Inspect(f, func(n ast.Node) bool {
		clit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		typIdent, ok := clit.Type.(*ast.Ident)
		if !ok || typIdent.Name != "replacementOpenRequest" {
			return true
		}
		foundReplacementOpenReq = true
		foundIsRetryPath := false
		for _, elt := range clit.Elts {
			kve, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			keyIdent, ok := kve.Key.(*ast.Ident)
			if !ok || keyIdent.Name != "isRetryPath" {
				continue
			}
			foundIsRetryPath = true
			valIdent, ok := kve.Value.(*ast.Ident)
			if !ok || valIdent.Name != "false" {
				pos := fset.Position(kve.Pos())
				t.Fatalf("agent_loop_guard_continuation.go:%d: replacementOpenRequest.isRetryPath must be false for continuation, got %v", pos.Line, kve.Value)
			}
		}
		if !foundIsRetryPath {
			pos := fset.Position(clit.Pos())
			t.Fatalf("agent_loop_guard_continuation.go:%d: replacementOpenRequest must explicitly set isRetryPath: false", pos.Line)
		}
		return true
	})

	if !foundReplacementOpenReq {
		t.Fatalf("did not find replacementOpenRequest in agent_loop_guard_continuation.go")
	}

	// Verify recovery_controller.go distinguishes isRetryPath for openModeGuardContinuation vs openModeRetry
	recFile := filepath.Join(runtimeDir, "recovery_controller.go")
	rf, err := parser.ParseFile(fset, recFile, nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse recovery_controller.go: %v", err)
	}

	foundGuardContinuationMode := false
	ast.Inspect(rf, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if ok && ident.Name == "openModeGuardContinuation" {
			foundGuardContinuationMode = true
		}
		return true
	})
	if !foundGuardContinuationMode {
		t.Fatalf("recovery_controller.go must reference openModeGuardContinuation for non-retry continuation paths")
	}
}

// TestHiddenRecoveryInstructionNeverPersistedAsUserAuthored verifies that
// automated recovery instructions in continuation legs are injected ONLY as
// RoleDeveloper (internal), and never as RoleUser or persisted to client-facing
// user-authored conversation history (requirements 6.5, 11.5; task 10.2).
func TestHiddenRecoveryInstructionNeverPersistedAsUserAuthored(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	contFile := filepath.Join(root, "internal", "core", "runtime", "agent_loop_guard_continuation.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, contFile, nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse agent_loop_guard_continuation.go: %v", err)
	}

	var checkedRoles []string
	ast.Inspect(f, func(n ast.Node) bool {
		// Look for Role assignments to newBaseline (Messages or Items)
		clit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		var typeName string
		switch tnode := clit.Type.(type) {
		case *ast.SelectorExpr:
			typeName = tnode.Sel.Name
		case *ast.Ident:
			typeName = tnode.Name
		}

		if typeName == "Message" || typeName == "Item" {
			for _, elt := range clit.Elts {
				kve, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				keyIdent, ok := kve.Key.(*ast.Ident)
				if !ok || keyIdent.Name != "Role" {
					continue
				}
				// Verify the role selector
				sel, ok := kve.Value.(*ast.SelectorExpr)
				if ok {
					roleName := sel.Sel.Name
					checkedRoles = append(checkedRoles, roleName)
					if roleName == "RoleUser" || roleName == "RoleAssistant" {
						pos := fset.Position(kve.Pos())
						t.Fatalf("agent_loop_guard_continuation.go:%d: automated recovery instruction must never use %s (must be RoleDeveloper/internal)", pos.Line, roleName)
					}
				}
			}
		}
		return true
	})

	if len(checkedRoles) == 0 {
		t.Fatalf("no Role declarations found in agent_loop_guard_continuation.go")
	}
	foundDeveloper := false
	for _, r := range checkedRoles {
		if r == "RoleDeveloper" {
			foundDeveloper = true
			break
		}
	}
	if !foundDeveloper {
		t.Fatalf("agent_loop_guard_continuation.go must explicitly use RoleDeveloper for hidden recovery instructions, found %v", checkedRoles)
	}
}
