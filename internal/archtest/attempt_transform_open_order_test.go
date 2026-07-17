package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

const runCandidateAttemptTransformStage = "RunCandidateAttemptTransformStage"

func findFunc(f *ast.File, name string) *ast.FuncDecl {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Body == nil {
			continue
		}
		if fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func firstCallPos(body *ast.BlockStmt, match func(pkg, name string, call *ast.CallExpr) bool) token.Pos {
	var pos token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		if pos != 0 {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		pkg, name := qualifiedCall(call.Fun)
		if match(pkg, name, call) {
			pos = call.Pos()
			return false
		}
		return true
	})
	return pos
}

func TestOpenPlannedCandidate_attemptTransformBetweenShapeAndCapabilities(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "core", "runtime", "executor_open_attempt.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	openFn := findFunc(f, "openPlannedCandidate")
	if openFn == nil {
		t.Fatal("openPlannedCandidate not found")
	}

	shapePos := firstCallPos(openFn.Body, func(_, name string, _ *ast.CallExpr) bool {
		return name == "shapeAttemptCall"
	})
	transformPos := firstCallPos(openFn.Body, func(_, name string, _ *ast.CallExpr) bool {
		return name == runCandidateAttemptTransformStage
	})
	capsPos := firstCallPos(openFn.Body, func(pkg, name string, _ *ast.CallExpr) bool {
		return pkg == "lipapi" && name == "RequiredCapabilities"
	})
	openPos := firstCallPos(openFn.Body, func(pkg, name string, call *ast.CallExpr) bool {
		if name != "Open" {
			return false
		}
		if pkg == "be" || pkg == "backend" {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		if id, ok := sel.X.(*ast.Ident); ok && (id.Name == "be" || id.Name == "backend") {
			return true
		}
		return false
	})

	if shapePos == 0 {
		t.Fatal("openPlannedCandidate must call shapeAttemptCall")
	}
	if capsPos == 0 {
		t.Fatal("openPlannedCandidate must call RequiredCapabilities")
	}
	if transformPos == 0 {
		t.Fatalf("RED: openPlannedCandidate must call %s after shapeAttemptCall and before RequiredCapabilities (stage %s)",
			runCandidateAttemptTransformStage, "candidate_attempt_transform")
	}
	if !(shapePos < transformPos && transformPos < capsPos) {
		t.Fatalf("want shapeAttemptCall < %s < RequiredCapabilities; positions shape=%d transform=%d caps=%d",
			runCandidateAttemptTransformStage, shapePos, transformPos, capsPos)
	}
	if openPos != 0 && !(capsPos < openPos) {
		t.Fatalf("RequiredCapabilities must precede backend Open; caps=%d open=%d", capsPos, openPos)
	}
}

func TestOpenPlannedCandidate_excludeCandidateDecisionHandledBeforeOpen(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "core", "runtime", "executor_open_attempt.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	openFn := findFunc(f, "openPlannedCandidate")
	if openFn == nil {
		t.Fatal("openPlannedCandidate not found")
	}

	transformPos := firstCallPos(openFn.Body, func(_, name string, _ *ast.CallExpr) bool {
		return name == runCandidateAttemptTransformStage
	})
	var excludedPos, openPos token.Pos
	ast.Inspect(openFn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if excludedPos == 0 && x.Sel != nil && x.Sel.Name == "Excluded" {
				excludedPos = x.Pos()
			}
		case *ast.CallExpr:
			if openPos != 0 {
				return true
			}
			pkg, name := qualifiedCall(x.Fun)
			if name != "Open" {
				return true
			}
			if pkg == "be" || pkg == "backend" {
				openPos = x.Pos()
				return true
			}
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && (id.Name == "be" || id.Name == "backend") {
					openPos = x.Pos()
				}
			}
		}
		return true
	})

	if transformPos == 0 {
		t.Fatalf("openPlannedCandidate must call %s before backend Open", runCandidateAttemptTransformStage)
	}
	if excludedPos == 0 {
		t.Fatal("openPlannedCandidate must branch on transform Excluded before backend Open so excluded candidates never open")
	}
	if openPos == 0 {
		t.Fatal("openPlannedCandidate must call backend Open")
	}
	if !(transformPos < openPos && excludedPos < openPos) {
		t.Fatalf("want transform+Excluded before backend Open; transform=%d excluded=%d open=%d", transformPos, excludedPos, openPos)
	}
}
