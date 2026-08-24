package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentLoopGuard_SteeringArchitecture_NoDirectAppendOrGuardHidden enforces
// the architecture ratchet for canonical conversation-view steering integration
// (Task 11.1 Point 9, Requirements 6.17, 12.16).
//
// This test asserts that:
//  1. turnTerminal struct in internal/core/runtime/turn_terminal.go does not contain
//     the legacy guardHidden field (conversation-view steering must be single authority).
//  2. internal/core/runtime/agent_loop_guard_continuation.go does not perform direct
//     slice append to Call.Messages or Call.Items (newBaseline.Messages/Items = append(...)).
//  3. internal/core/runtime/agent_loop_guard_continuation.go imports and uses canonical
//     steering (pkg/lipsdk/steering or internal/core/conversationview).
//
// This architecture ratchet enforces single-authority conversation-view
// steering integration (Task 11.4 / 11.5, Requirements 6.17, 12.16).
func TestAgentLoopGuard_SteeringArchitecture_NoDirectAppendOrGuardHidden(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	// 1. Check turn_terminal.go for legacy guardHidden field
	terminalPath := filepath.Join(root, "internal", "core", "runtime", "turn_terminal.go")
	fset := token.NewFileSet()
	tf, err := parser.ParseFile(fset, terminalPath, nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse turn_terminal.go: %v", err)
	}

	foundGuardHidden := false
	ast.Inspect(tf, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "turnTerminal" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				if name.Name == "guardHidden" {
					foundGuardHidden = true
					pos := fset.Position(name.Pos())
					t.Logf("found legacy guardHidden field in turnTerminal at %s:%d", filepath.Base(terminalPath), pos.Line)
				}
			}
		}
		return false
	})

	if foundGuardHidden {
		t.Errorf("ARCHITECTURE RED: turnTerminal still contains legacy guardHidden field; conversation-view steering must be the single authority for hidden control content (Req 6.17, 12.16)")
	}

	// 2. Check agent_loop_guard_continuation.go for direct Call.Messages / Items appends
	contPath := filepath.Join(root, "internal", "core", "runtime", "agent_loop_guard_continuation.go")
	cf, err := parser.ParseFile(fset, contPath, nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse agent_loop_guard_continuation.go: %v", err)
	}

	foundDirectAppend := false
	ast.Inspect(cf, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if sel.Sel.Name == "Messages" || sel.Sel.Name == "Items" {
				for _, rhs := range assign.Rhs {
					call, ok := rhs.(*ast.CallExpr)
					if !ok {
						continue
					}
					ident, ok := call.Fun.(*ast.Ident)
					if ok && ident.Name == "append" {
						foundDirectAppend = true
						pos := fset.Position(assign.Pos())
						t.Logf("found direct append to Call.%s at %s:%d", sel.Sel.Name, filepath.Base(contPath), pos.Line)
					}
				}
			}
		}
		return true
	})

	if foundDirectAppend {
		t.Errorf("ARCHITECTURE RED: agent_loop_guard_continuation.go contains direct append to Call.Messages/Items; all hidden control content must be registered via pkg/lipsdk/steering.Writer (Req 6.17, 12.16)")
	}

	// 3. Check for canonical steering imports in agent_loop_guard_continuation.go
	src, err := os.ReadFile(contPath)
	if err != nil {
		t.Fatalf("read agent_loop_guard_continuation.go: %v", err)
	}
	srcStr := string(src)
	hasSteeringImport := strings.Contains(srcStr, "pkg/lipsdk/steering") || strings.Contains(srcStr, "internal/core/conversationview")
	if !hasSteeringImport {
		t.Errorf("ARCHITECTURE RED: agent_loop_guard_continuation.go must import and use pkg/lipsdk/steering or internal/core/conversationview for recovery instructions (Req 6.8, 6.17)")
	}
}
