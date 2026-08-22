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

// TestAttemptSessionInnerFence ensures the attemptSession inner stream is
// accessed only through its owner methods. Direct field access outside
// attempt_session.go bypasses authority, B-leg and observation terminalization
// and is the exact gap the P1 assembly fix closes.
func TestAttemptSessionInnerFence(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	runtimeDir := filepath.Join(root, "internal", "core", "runtime")
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		t.Fatalf("read runtime dir: %v", err)
	}
	var violations []string
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if name == "attempt_session.go" {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		path := filepath.Join(runtimeDir, name)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "inner" {
				return true
			}
			// Any .inner selector in runtime production code outside attempt_session.go
			// is a fence violation. The owner exposes loadInner/storeInner/takeInner
			// and TerminalizeAttempt; direct field access bypasses them.
			pos := fset.Position(sel.Pos())
			violations = append(violations, filepath.ToSlash(name)+":"+itoa(pos.Line)+": direct .inner access (use loadInner/storeInner/takeInner or TerminalizeAttempt)")
			return true
		})
	}
	if len(violations) > 0 {
		t.Fatalf("attemptSession inner fence violated (%d):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

func itoa(n int) string {
	// avoid importing strconv just for one helper
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
