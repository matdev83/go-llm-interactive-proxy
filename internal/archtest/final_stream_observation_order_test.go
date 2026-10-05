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
		return name == "emitTrafficPTCFinal" || name == emitClientFacingObserved
	})
	return obsPos, emitPos
}

func parseRuntimeFunction(t *testing.T, root, filename, name string) *ast.FuncDecl {
	t.Helper()
	path := filepath.Join(root, "internal", "core", "runtime", filename)
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	fn := findFunc(f, name)
	if fn == nil {
		t.Fatalf("%s not found in %s", name, filename)
	}
	return fn
}

func assertResponseObservationOrder(t *testing.T, fn *ast.FuncDecl) (obsPos, emitPos, rememberPos token.Pos) {
	t.Helper()
	obsPos, emitPos = firstObservationEmitPos(fn.Body)
	rememberPos = firstCallPos(fn.Body, func(_, name string, _ *ast.CallExpr) bool {
		return name == "rememberClientEvent"
	})
	if obsPos == 0 {
		t.Fatalf("%s must call %s before response output side effects", fn.Name.Name, runFinalStreamObservationStage)
	}
	if emitPos == 0 {
		t.Fatalf("%s must emit response traffic after final observation", fn.Name.Name)
	}
	if rememberPos == 0 {
		t.Fatalf("%s must remember client events after final observation", fn.Name.Name)
	}
	if obsPos >= emitPos || obsPos >= rememberPos {
		t.Fatalf("want observation < emit/remember; obs=%d emit=%d remember=%d", obsPos, emitPos, rememberPos)
	}
	return obsPos, emitPos, rememberPos
}

func TestResponsePipeline_transformObserveFinalStreamObservationAfterHooksBeforeEmit(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	recv := parseRuntimeFunction(t, root, "executor_recv_loop.go", "Recv")
	transform := parseRuntimeFunction(t, root, "response_pipeline_observations.go", "transformClientEvent")
	observe := parseRuntimeFunction(t, root, "response_pipeline_observations.go", "observeClientFacing")
	hooksPos := firstCallPos(transform.Body, func(_, name string, _ *ast.CallExpr) bool { return name == "RunResponsePartHooks" })
	assertResponseObservationOrder(t, observe)
	transformPos := firstCallPos(recv.Body, func(_, name string, _ *ast.CallExpr) bool { return name == "transformClientEvent" })
	observePos := firstCallPos(recv.Body, func(_, name string, _ *ast.CallExpr) bool { return name == "observeClientFacing" })
	if hooksPos == 0 {
		t.Fatal("transformClientEvent must call RunResponsePartHooks")
	}
	if transformPos == 0 || observePos == 0 || transformPos >= observePos {
		t.Fatalf("Recv must transform response hooks before delegated observation; transform=%d observe=%d", transformPos, observePos)
	}
}

func TestResponsePipeline_gatedObservationBeforeEmit(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	recv := parseRuntimeFunction(t, root, "executor_recv_loop.go", "Recv")
	gate := parseRuntimeFunction(t, root, "response_pipeline_observations.go", "applyCompletionGates")
	observe := parseRuntimeFunction(t, root, "response_pipeline_observations.go", "observeClientFacing")
	gatePos := firstCallPos(gate.Body, func(_, name string, _ *ast.CallExpr) bool { return name == "completionGatedEmit" })
	preflightPos := firstCallPos(gate.Body, func(_, name string, _ *ast.CallExpr) bool { return name == "recordClientFacing" })
	assertResponseObservationOrder(t, observe)
	applyPos := firstCallPos(recv.Body, func(_, name string, _ *ast.CallExpr) bool { return name == "applyCompletionGates" })
	observePos := firstCallPos(recv.Body, func(_, name string, _ *ast.CallExpr) bool { return name == "observeClientFacing" })
	if gatePos == 0 {
		t.Fatal("applyCompletionGates must resolve gates before client observation")
	}
	if preflightPos == 0 || gatePos >= preflightPos {
		t.Fatalf("want gate-resolution < gate preflight; gate=%d preflight=%d", gatePos, preflightPos)
	}
	if applyPos == 0 || observePos == 0 || applyPos >= observePos {
		t.Fatalf("Recv must resolve gates before delegated observation; apply=%d observe=%d", applyPos, observePos)
	}
}

func TestRecv_responseFinishedObservationBeforeEmit(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	recv := parseRuntimeFunction(t, root, "executor_recv_loop.go", "Recv")
	observe := parseRuntimeFunction(t, root, "response_pipeline_observations.go", "observeClientFacing")
	finalizePos := firstCallPos(recv.Body, func(_, name string, _ *ast.CallExpr) bool { return name == "finalizeResponseFinishedAuthority" })
	observePos := firstCallPos(recv.Body, func(_, name string, _ *ast.CallExpr) bool { return name == "observeClientFacing" })
	obsPos, emitPos, rememberPos := assertResponseObservationOrder(t, observe)
	if finalizePos == 0 || observePos == 0 || finalizePos >= observePos {
		t.Fatalf("Recv must finalize response authority before delegated observation; finalize=%d observe=%d", finalizePos, observePos)
	}
	if obsPos >= emitPos || obsPos >= rememberPos {
		t.Fatalf("response_finished observation must precede output and remember; obs=%d emit=%d remember=%d", obsPos, emitPos, rememberPos)
	}
}

func TestRuntimeFinalStreamOutcomes_freezeGateReplacedAndSuccessReleased(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "core", "runtime")
	fset := token.NewFileSet()
	//nolint:staticcheck // SA1019: intentional lightweight AST scan of one package dir
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
