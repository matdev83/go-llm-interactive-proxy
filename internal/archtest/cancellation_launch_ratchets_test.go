package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// DetectNonLinearizedBackendOpen scans runtime production code and verifies that
// all Backend.Open calls are guarded by A-leg launch authority (BeginBLegLaunch).
func DetectNonLinearizedBackendOpen(root string) ([]string, error) {
	runtimeDir := filepath.Join(root, "internal", "core", "runtime")
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		return nil, fmt.Errorf("read runtime dir: %w", err)
	}

	var violations []string
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(runtimeDir, ent.Name())
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", ent.Name(), err)
		}
		violations = append(violations, inspectBackendOpenAST(fset, file, ent.Name())...)
	}
	return violations, nil
}

func inspectBackendOpenAST(fset *token.FileSet, file *ast.File, relPath string) []string {
	var violations []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		var beginLaunchPos token.Pos

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			// Check for BeginBLegLaunch call
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "BeginBLegLaunch" {
				beginLaunchPos = call.Pos()
				return true
			}

			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Open" {
				return true
			}

			// Identify if this is a backend Open call (any .Open call not on finalStreamObserver)
			receiverText := nodeText(sel.X)
			isBackendOpen := !strings.Contains(receiverText, "finalStreamObs")

			if !isBackendOpen {
				return true
			}

			pos := fset.Position(call.Pos())

			// Rule 1: Production Backend.Open must be in executor_open_attempt.go inside openAttemptTx
			if relPath != "executor_open_attempt.go" || fn.Name.Name != "openAttemptTx" {
				violations = append(violations, fmt.Sprintf("%s:%d: Backend.Open called outside openAttemptTx in %s (must be in executor_open_attempt.go:openAttemptTx)", relPath, pos.Line, fn.Name.Name))
				return true
			}

			// Rule 2: Inside openAttemptTx, BeginBLegLaunch must precede Backend.Open
			if beginLaunchPos == token.NoPos || beginLaunchPos >= call.Pos() {
				violations = append(violations, fmt.Sprintf("%s:%d: Backend.Open called without preceding BeginBLegLaunch launch authority", relPath, pos.Line))
			}
			return true
		})
	}
	return violations
}

// DetectRawRegistrationInRuntime scans runtime production code to ensure all A-leg
// registrations and launch permit commits use ready.lifecycleHandle(), never raw streams or raw sessions.
func DetectRawRegistrationInRuntime(root string) ([]string, error) {
	runtimeDir := filepath.Join(root, "internal", "core", "runtime")
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		return nil, fmt.Errorf("read runtime dir: %w", err)
	}

	var violations []string
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
			continue
		}
		if ent.Name() == "attempt_session.go" {
			continue
		}
		path := filepath.Join(runtimeDir, ent.Name())
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", ent.Name(), err)
		}
		violations = append(violations, inspectRegistrationAST(fset, file, ent.Name())...)
	}
	return violations, nil
}

func inspectRegistrationAST(fset *token.FileSet, file *ast.File, relPath string) []string {
	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		if sel.Sel.Name == "RegisterBLeg" {
			pos := fset.Position(call.Pos())
			for _, arg := range call.Args {
				argText := nodeText(arg)
				if strings.Contains(argText, "sess.lifecycleHandle") ||
					strings.Contains(argText, "session.lifecycleHandle") ||
					strings.Contains(argText, "stream") ||
					strings.Contains(argText, "inner") {
					violations = append(violations, fmt.Sprintf("%s:%d: RegisterBLeg called with raw attempt/stream handle (must use ready.lifecycleHandle): %s", relPath, pos.Line, argText))
				}
			}
		}

		if sel.Sel.Name == "Commit" {
			pos := fset.Position(call.Pos())
			recvText := nodeText(sel.X)
			if strings.Contains(recvText, "launchPermit") || strings.Contains(recvText, "permit") {
				if len(call.Args) > 0 {
					argText := nodeText(call.Args[0])
					if !strings.Contains(argText, "ready.lifecycleHandle") {
						violations = append(violations, fmt.Sprintf("%s:%d: LaunchPermit.Commit called with non-ready handle %q (must use ready.lifecycleHandle)", relPath, pos.Line, argText))
					}
				}
			}
		}

		return true
	})
	return violations
}

// Tests for Launch Authority and Registration Ratchets

func TestArch_LaunchAuthority_LinearizedOpen(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	violations, err := DetectNonLinearizedBackendOpen(root)
	if err != nil {
		t.Fatalf("DetectNonLinearizedBackendOpen failed: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("Launch authority ratchet violated (%d violations):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

func TestArch_LaunchAuthority_LinearizedOpen_NegativeFixtures(t *testing.T) {
	t.Parallel()

	// Fixture A: Backend.Open called outside openAttemptTx
	badSourceA := `package runtime
import "context"
func otherFunc(ctx context.Context, be backend) {
	_, _ = be.Open(ctx, nil, nil)
}`
	fsetA := token.NewFileSet()
	fileA, err := parser.ParseFile(fsetA, "other_file.go", badSourceA, 0)
	if err != nil {
		t.Fatalf("parse fixture A: %v", err)
	}
	violationsA := inspectBackendOpenAST(fsetA, fileA, "other_file.go")
	if len(violationsA) == 0 {
		t.Fatal("expected fixture A (Backend.Open outside openAttemptTx) to be rejected")
	}

	// Fixture B: openAttemptTx calling Backend.Open without BeginBLegLaunch
	badSourceB := `package runtime
import "context"
func (e *Executor) openAttemptTx(ctx context.Context, tx *attemptTx) error {
	stream, err := be.Open(ctx, nil, nil)
	return err
}`
	fsetB := token.NewFileSet()
	fileB, err := parser.ParseFile(fsetB, "executor_open_attempt.go", badSourceB, 0)
	if err != nil {
		t.Fatalf("parse fixture B: %v", err)
	}
	violationsB := inspectBackendOpenAST(fsetB, fileB, "executor_open_attempt.go")
	if len(violationsB) == 0 {
		t.Fatal("expected fixture B (openAttemptTx without BeginBLegLaunch) to be rejected")
	}

	// Fixture C: Valid openAttemptTx with BeginBLegLaunch before Backend.Open
	goodSourceC := `package runtime
import "context"
func (e *Executor) openAttemptTx(ctx context.Context, tx *attemptTx) error {
	permitCtx, permit, perr := tx.reqFacts.aScope.BeginBLegLaunch(ctx, tx.bleg.BLegID)
	if perr != nil { return perr }
	stream, err := be.Open(permitCtx, nil, nil)
	return err
}`
	fsetC := token.NewFileSet()
	fileC, err := parser.ParseFile(fsetC, "executor_open_attempt.go", goodSourceC, 0)
	if err != nil {
		t.Fatalf("parse fixture C: %v", err)
	}
	violationsC := inspectBackendOpenAST(fsetC, fileC, "executor_open_attempt.go")
	if len(violationsC) != 0 {
		t.Fatalf("expected valid fixture C to pass, got violations: %v", violationsC)
	}

	// Fixture D: custom backend call in arbitrary file
	badSourceD := `package runtime
import "context"
func runCustom(ctx context.Context, customBackend any) {
	_, _ = customBackend.Open(ctx, nil, nil)
}`
	fsetD := token.NewFileSet()
	fileD, err := parser.ParseFile(fsetD, "custom.go", badSourceD, 0)
	if err != nil {
		t.Fatalf("parse fixture D: %v", err)
	}
	violationsD := inspectBackendOpenAST(fsetD, fileD, "custom.go")
	if len(violationsD) == 0 {
		t.Fatal("expected fixture D (customBackend.Open in custom.go) to be rejected")
	}
}

func TestArch_UnpublishedRegistration_ReadyLifecycleHandleOnly(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	violations, err := DetectRawRegistrationInRuntime(root)
	if err != nil {
		t.Fatalf("DetectRawRegistrationInRuntime failed: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("Unpublished registration ratchet violated (%d violations):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

func TestArch_UnpublishedRegistration_NegativeFixtures(t *testing.T) {
	t.Parallel()

	// Fixture A: LaunchPermit.Commit called with raw stream
	badSourceA := `package runtime
func (e *Executor) finish(tx *attemptTx) {
	tx.launchPermit.Commit(tx.stream)
}`
	fsetA := token.NewFileSet()
	fileA, err := parser.ParseFile(fsetA, "finish.go", badSourceA, 0)
	if err != nil {
		t.Fatalf("parse fixture A: %v", err)
	}
	violationsA := inspectRegistrationAST(fsetA, fileA, "finish.go")
	if len(violationsA) == 0 {
		t.Fatal("expected fixture A (Commit with raw stream) to be rejected")
	}

	// Fixture B: RegisterBLeg called with sess.lifecycleHandle directly
	badSourceB := `package runtime
func (e *Executor) reg(aScope *ALeg, sess *attemptSession) {
	aScope.RegisterBLeg(ctx, leglifecycle.BLegHandle{ID: "b1", Attempt: sess.lifecycleHandle()})
}`
	fsetB := token.NewFileSet()
	fileB, err := parser.ParseFile(fsetB, "reg.go", badSourceB, 0)
	if err != nil {
		t.Fatalf("parse fixture B: %v", err)
	}
	violationsB := inspectRegistrationAST(fsetB, fileB, "reg.go")
	if len(violationsB) == 0 {
		t.Fatal("expected fixture B (RegisterBLeg with sess.lifecycleHandle) to be rejected")
	}

	// Fixture C: Valid Commit with ready.lifecycleHandle()
	goodSourceC := `package runtime
func (e *Executor) finish(tx *attemptTx, ready *readyAttempt) {
	tx.launchPermit.Commit(ready.lifecycleHandle())
}`
	fsetC := token.NewFileSet()
	fileC, err := parser.ParseFile(fsetC, "finish.go", goodSourceC, 0)
	if err != nil {
		t.Fatalf("parse fixture C: %v", err)
	}
	violationsC := inspectRegistrationAST(fsetC, fileC, "finish.go")
	if len(violationsC) != 0 {
		t.Fatalf("expected valid fixture C to pass, got violations: %v", violationsC)
	}

	// Fixture D: RegisterBLeg called with raw stream handle directly
	badSourceD := `package runtime
func (e *Executor) regStream(aScope *ALeg, stream lipapi.ManagedEventStream) {
	aScope.RegisterBLeg(ctx, leglifecycle.BLegHandle{ID: "b1", Attempt: stream})
}`
	fsetD := token.NewFileSet()
	fileD, err := parser.ParseFile(fsetD, "reg_stream.go", badSourceD, 0)
	if err != nil {
		t.Fatalf("parse fixture D: %v", err)
	}
	violationsD := inspectRegistrationAST(fsetD, fileD, "reg_stream.go")
	if len(violationsD) == 0 {
		t.Fatal("expected fixture D (RegisterBLeg with raw stream) to be rejected")
	}
}
