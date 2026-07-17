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

const (
	runFinalStreamObservationStage = "RunFinalStreamObservationStage"
	emitClientFacingObserved       = "emitClientFacingObserved"
)

func firstObservationEmitPos(body *ast.BlockStmt) (obsPos, emitPos token.Pos) {
	obsPos = firstCallPos(body, func(_, name string, _ *ast.CallExpr) bool {
		return name == runFinalStreamObservationStage || name == emitClientFacingObserved
	})
	emitPos = firstCallPos(body, func(_, name string, _ *ast.CallExpr) bool {
		return name == "beforeEmitClientFacing" || name == emitClientFacingObserved
	})
	return obsPos, emitPos
}

func TestDispatchClientFacingEvent_finalStreamObservationAfterHooksBeforeEmit(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "core", "runtime", "executor_recv_handlers.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fn := findFunc(f, "dispatchClientFacingEvent")
	if fn == nil {
		t.Fatal("dispatchClientFacingEvent not found")
	}

	hooksPos := firstCallPos(fn.Body, func(pkg, name string, _ *ast.CallExpr) bool {
		return name == "RunResponsePartHooks" || (pkg == "bus" && name == "RunResponsePartHooks")
	})
	if hooksPos == 0 {
		hooksPos = firstCallPos(fn.Body, func(_, name string, call *ast.CallExpr) bool {
			if name != "RunResponsePartHooks" {
				return false
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			return ok && sel.Sel != nil && sel.Sel.Name == "RunResponsePartHooks"
		})
	}
	obsPos, emitPos := firstObservationEmitPos(fn.Body)

	if hooksPos == 0 {
		t.Fatal("dispatchClientFacingEvent must call RunResponsePartHooks")
	}
	if emitPos == 0 {
		t.Fatal("dispatchClientFacingEvent must call beforeEmitClientFacing or emitClientFacingObserved")
	}
	if obsPos == 0 {
		t.Fatalf("RED: dispatchClientFacingEvent must call %s or %s after RunResponsePartHooks and before client emit",
			runFinalStreamObservationStage, emitClientFacingObserved)
	}
	if !(hooksPos < obsPos && obsPos <= emitPos) {
		t.Fatalf("want RunResponsePartHooks < observation/emit <= client emit; positions hooks=%d obs=%d emit=%d",
			hooksPos, obsPos, emitPos)
	}
}

func TestHandleGatedPath_finalStreamObservationBeforeEmit(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "core", "runtime", "executor_recv_handlers.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fn := findFunc(f, "handleGatedPath")
	if fn == nil {
		t.Fatal("handleGatedPath not found")
	}

	gatePos := firstCallPos(fn.Body, func(_, name string, _ *ast.CallExpr) bool {
		return name == "completionGatedEmit" || name == "emitGateDrained"
	})
	obsPos, emitPos := firstObservationEmitPos(fn.Body)

	if gatePos == 0 {
		t.Fatal("handleGatedPath must call completionGatedEmit or emitGateDrained")
	}
	if emitPos == 0 {
		t.Fatal("handleGatedPath must call beforeEmitClientFacing or emitClientFacingObserved")
	}
	if obsPos == 0 {
		t.Fatalf("RED: handleGatedPath must call %s or %s after gate resolution and before client emit",
			runFinalStreamObservationStage, emitClientFacingObserved)
	}
	if !(gatePos < obsPos && obsPos <= emitPos) {
		t.Fatalf("want gate-resolution < observation/emit <= client emit; positions gate=%d obs=%d emit=%d",
			gatePos, obsPos, emitPos)
	}
}

func TestHandleResponseFinishedPath_finalStreamObservationBeforeEmit(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "core", "runtime", "executor_recv_handlers.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fn := findFunc(f, "handleResponseFinishedPath")
	if fn == nil {
		t.Fatal("handleResponseFinishedPath not found")
	}

	obsPos, emitPos := firstObservationEmitPos(fn.Body)
	if emitPos == 0 {
		t.Fatal("handleResponseFinishedPath must call beforeEmitClientFacing or emitClientFacingObserved")
	}
	if obsPos == 0 {
		t.Fatalf("RED: handleResponseFinishedPath must call %s or %s before client emit so success_released observes released response_finished",
			runFinalStreamObservationStage, emitClientFacingObserved)
	}
	if !(obsPos <= emitPos) {
		t.Fatalf("want observation/emit before client release; obs=%d emit=%d", obsPos, emitPos)
	}
}

func TestRuntimeFinalStreamOutcomes_freezeGateReplacedAndSuccessReleased(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "core", "runtime")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, path, func(info os.FileInfo) bool {
		name := info.Name()
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	}, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse dir: %v", err)
	}
	var sawGateReplaced, sawSuccessReleased bool
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil {
					return true
				}
				switch sel.Sel.Name {
				case "OutcomeGateReplaced":
					sawGateReplaced = true
				case "OutcomeSuccessReleased":
					sawSuccessReleased = true
				}
				return true
			})
		}
	}
	if !sawGateReplaced {
		t.Error("RED: runtime must finalize gate-replaced observers with response.OutcomeGateReplaced")
	}
	if !sawSuccessReleased {
		t.Error("RED: runtime must finalize successful release with response.OutcomeSuccessReleased after response_finished")
	}
}
