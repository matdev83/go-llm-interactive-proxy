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

// DetectAlternateAttemptTerminalOwners verifies that TerminalizeAttempt on attemptSession
// is the sole production attempt terminal owner.
func DetectAlternateAttemptTerminalOwners(root string) ([]string, error) {
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
		violations = append(violations, inspectAttemptTerminalAST(fset, file, ent.Name())...)
	}
	return violations, nil
}

func inspectAttemptTerminalAST(fset *token.FileSet, file *ast.File, relPath string) []string {
	var violations []string

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		pos := fset.Position(fn.Pos())

		// Check receiver methods on attemptSession / attemptTx
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			recvType := nodeText(fn.Recv.List[0].Type)
			if strings.Contains(recvType, "attemptSession") || strings.Contains(recvType, "attemptTx") {
				switch fn.Name.Name {
				case "cancelAndClose", "AbortBeforeReturn", "finishAsReplaced", "Rollback", "Abort", "RollbackParallelLoser", "terminalizeAttemptEphemeral":
					violations = append(violations, fmt.Sprintf("%s:%d: forbidden terminal method %s on %s", relPath, pos.Line, fn.Name.Name, recvType))
				}
			}
		}

		// Check function names in turn_terminal.go
		if relPath == "turn_terminal.go" {
			switch fn.Name.Name {
			case "closeBackend", "cancelForClose":
				violations = append(violations, fmt.Sprintf("%s:%d: forbidden helper %s declared in turn_terminal.go", relPath, pos.Line, fn.Name.Name))
			}
		}
	}

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pos := fset.Position(call.Pos())

		// Direct .terminal.Terminalize is allowed only in attempt_session.go (on streamTerminal)
		if sel.Sel.Name == "Terminalize" && nodeText(sel.X) == "a.terminal" {
			if relPath != "attempt_session.go" {
				violations = append(violations, fmt.Sprintf("%s:%d: direct attempt terminal.Terminalize call outside attempt_session.go", relPath, pos.Line))
			}
		}

		// In parallel_race.go, releaseLosers must not invoke raw cleanup calls
		if relPath == "parallel_race.go" {
			switch sel.Sel.Name {
			case "cancelLosers", "releaseBLegs", "recordParallelBillingLeg", "terminalizeAttemptEphemeral":
				violations = append(violations, fmt.Sprintf("%s:%d: forbidden raw cleanup call in parallel_race.go: %s", relPath, pos.Line, sel.Sel.Name))
			}
		}

		return true
	})

	return violations
}

// DetectPostPublicationRawTeardown verifies that no raw stream teardown occurs after publication.
func DetectPostPublicationRawTeardown(root string) ([]string, error) {
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
		violations = append(violations, inspectPostPublicationTeardownAST(fset, file, ent.Name())...)
	}
	return violations, nil
}

func inspectPostPublicationTeardownAST(fset *token.FileSet, file *ast.File, relPath string) []string {
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
		pos := fset.Position(call.Pos())

		switch sel.Sel.Name {
		case "detachStream", "cancelForClose", "closeBackend", "closeThinkerInner", "closeActiveInner":
			violations = append(violations, fmt.Sprintf("%s:%d: forbidden post-publication raw teardown call: %s", relPath, pos.Line, sel.Sel.Name))
		}
		return true
	})
	return violations
}

// DetectLockHeldIOOrCoordination verifies that no locks are held during I/O or coordination (Req 3.7).
func DetectLockHeldIOOrCoordination(root string) ([]string, error) {
	var violations []string

	// 1. Check leglifecycle/coordinator.go
	legDir := filepath.Join(root, "internal", "core", "leglifecycle")
	legPath := filepath.Join(legDir, "coordinator.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, legPath, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse coordinator.go: %w", err)
	}
	violations = append(violations, inspectLockHeldIOAST(fset, file, "coordinator.go")...)

	// 2. Check runtime/attempt_session.go
	runtimeDir := filepath.Join(root, "internal", "core", "runtime")
	sessPath := filepath.Join(runtimeDir, "attempt_session.go")
	sessFile, err := parser.ParseFile(fset, sessPath, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse attempt_session.go: %w", err)
	}
	violations = append(violations, inspectLockHeldIOAST(fset, sessFile, "attempt_session.go")...)

	return violations, nil
}

func inspectLockHeldIOAST(fset *token.FileSet, file *ast.File, relPath string) []string {
	var violations []string

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		var lockActive bool
		var lockPos token.Pos
		var lockVar string

		for _, stmt := range fn.Body.List {
			ast.Inspect(stmt, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}

				// Check Lock / Unlock selectors
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if sel.Sel.Name == "Lock" {
						lockActive = true
						lockPos = call.Pos()
						lockVar = nodeText(sel.X)
					}
					if sel.Sel.Name == "Unlock" {
						lockActive = false
					}
				}

				if lockActive {
					callName := ""
					recvText := ""
					if ident, ok := call.Fun.(*ast.Ident); ok {
						callName = ident.Name
					} else if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						callName = sel.Sel.Name
						recvText = nodeText(sel.X)
					}

					if callName != "Lock" && callName != "Unlock" {
						pos := fset.Position(call.Pos())
						switch callName {
						case "cancelAndClose", "FinalizeBilling", "Open", "RunFinalStreamObservationStage":
							violations = append(violations, fmt.Sprintf("%s:%d: forbidden I/O call %s while %s is locked (line %d)", relPath, pos.Line, callName, lockVar, fset.Position(lockPos).Line))
						case "Cancel", "Close", "Recv":
							if strings.Contains(recvText, "Stream") || strings.Contains(recvText, "inner") || recvText == "b" || strings.Contains(recvText, "Attempt") || strings.Contains(recvText, "Backend") {
								violations = append(violations, fmt.Sprintf("%s:%d: forbidden stream I/O call %s.%s while %s is locked", relPath, pos.Line, recvText, callName, lockVar))
							}
						}
					}
				}
				return true
			})
		}
	}

	return violations
}

// Tests for Attempt Ownership, Post-Pub Teardown, and Lock-Held IO Ratchets

func TestArch_AttemptTerminal_SingleOwnerTerminalizeAttempt(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	violations, err := DetectAlternateAttemptTerminalOwners(root)
	if err != nil {
		t.Fatalf("DetectAlternateAttemptTerminalOwners failed: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("Single attempt terminal owner ratchet violated (%d violations):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

func TestArch_AttemptTerminal_NegativeFixtures(t *testing.T) {
	t.Parallel()

	// Fixture A: forbidden AbortBeforeReturn declared on attemptSession
	badSourceA := `package runtime
func (a *attemptSession) AbortBeforeReturn() {}`
	fsetA := token.NewFileSet()
	fileA, err := parser.ParseFile(fsetA, "attempt_session.go", badSourceA, 0)
	if err != nil {
		t.Fatalf("parse fixture A: %v", err)
	}
	violationsA := inspectAttemptTerminalAST(fsetA, fileA, "attempt_session.go")
	if len(violationsA) == 0 {
		t.Fatal("expected fixture A (AbortBeforeReturn on attemptSession) to be rejected")
	}

	// Fixture B: direct a.terminal.Terminalize call in executor_open_attempt.go
	badSourceB := `package runtime
func (e *Executor) doTerminal(a *attemptSession) {
	a.terminal.Terminalize(ctx, nil, nil, nil)
}`
	fsetB := token.NewFileSet()
	fileB, err := parser.ParseFile(fsetB, "executor_open_attempt.go", badSourceB, 0)
	if err != nil {
		t.Fatalf("parse fixture B: %v", err)
	}
	violationsB := inspectAttemptTerminalAST(fsetB, fileB, "executor_open_attempt.go")
	if len(violationsB) == 0 {
		t.Fatal("expected fixture B (direct Terminalize outside attempt_session.go) to be rejected")
	}

	// Fixture C: forbidden closeBackend declared in turn_terminal.go
	badSourceC := `package runtime
func (t *turnTerminal) closeBackend() {}`
	fsetC := token.NewFileSet()
	fileC, err := parser.ParseFile(fsetC, "turn_terminal.go", badSourceC, 0)
	if err != nil {
		t.Fatalf("parse fixture C: %v", err)
	}
	violationsC := inspectAttemptTerminalAST(fsetC, fileC, "turn_terminal.go")
	if len(violationsC) == 0 {
		t.Fatal("expected fixture C (closeBackend in turn_terminal.go) to be rejected")
	}

	// Fixture D: forbidden cancelLosers in parallel_race.go
	badSourceD := `package runtime
func (e *Executor) release(arms []parallelArmOutcome) {
	e.cancelLosers(arms)
}`
	fsetD := token.NewFileSet()
	fileD, err := parser.ParseFile(fsetD, "parallel_race.go", badSourceD, 0)
	if err != nil {
		t.Fatalf("parse fixture D: %v", err)
	}
	violationsD := inspectAttemptTerminalAST(fsetD, fileD, "parallel_race.go")
	if len(violationsD) == 0 {
		t.Fatal("expected fixture D (cancelLosers in parallel_race.go) to be rejected")
	}
}

func TestArch_NoPostPublicationRawTeardown(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	violations, err := DetectPostPublicationRawTeardown(root)
	if err != nil {
		t.Fatalf("DetectPostPublicationRawTeardown failed: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("Post-publication raw teardown ratchet violated (%d violations):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

func TestArch_NoPostPublicationRawTeardown_NegativeFixtures(t *testing.T) {
	t.Parallel()

	// Fixture A: cancelForClose
	badSourceA := `package runtime
func (s *retryRecvStream) closeStream() {
	s.cancelForClose()
}`
	fsetA := token.NewFileSet()
	fileA, err := parser.ParseFile(fsetA, "executor_retry_stream.go", badSourceA, 0)
	if err != nil {
		t.Fatalf("parse fixture A: %v", err)
	}
	violationsA := inspectPostPublicationTeardownAST(fsetA, fileA, "executor_retry_stream.go")
	if len(violationsA) == 0 {
		t.Fatal("expected fixture A (cancelForClose call) to be rejected")
	}

	// Fixture B: detachStream
	badSourceB := `package runtime
func (s *retryRecvStream) detach() {
	s.detachStream()
}`
	fsetB := token.NewFileSet()
	fileB, err := parser.ParseFile(fsetB, "executor_retry_stream.go", badSourceB, 0)
	if err != nil {
		t.Fatalf("parse fixture B: %v", err)
	}
	violationsB := inspectPostPublicationTeardownAST(fsetB, fileB, "executor_retry_stream.go")
	if len(violationsB) == 0 {
		t.Fatal("expected fixture B (detachStream call) to be rejected")
	}

	// Fixture C: closeThinkerInner
	badSourceC := `package runtime
func (s *interleavedStream) close() {
	s.closeThinkerInner()
}`
	fsetC := token.NewFileSet()
	fileC, err := parser.ParseFile(fsetC, "interleaved_stream.go", badSourceC, 0)
	if err != nil {
		t.Fatalf("parse fixture C: %v", err)
	}
	violationsC := inspectPostPublicationTeardownAST(fsetC, fileC, "interleaved_stream.go")
	if len(violationsC) == 0 {
		t.Fatal("expected fixture C (closeThinkerInner call) to be rejected")
	}
}

func TestArch_NoLockHeldIOOrCoordination(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	violations, err := DetectLockHeldIOOrCoordination(root)
	if err != nil {
		t.Fatalf("DetectLockHeldIOOrCoordination failed: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("Lock-held I/O ratchet violated (%d violations):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

func TestArch_NoLockHeldIOOrCoordination_NegativeFixtures(t *testing.T) {
	t.Parallel()

	// Fixture A: calling cancelAndClose while a.mu is locked
	badSourceA := `package leglifecycle
func (a *ALeg) Cancel(ctx context.Context, cause CancelCause) error {
	a.mu.Lock()
	cancelAndClose(ctx, a.cancelTimeout(), a.blegs["b1"], cause)
	a.mu.Unlock()
	return nil
}`
	fsetA := token.NewFileSet()
	fileA, err := parser.ParseFile(fsetA, "coordinator.go", badSourceA, 0)
	if err != nil {
		t.Fatalf("parse fixture A: %v", err)
	}
	violationsA := inspectLockHeldIOAST(fsetA, fileA, "coordinator.go")
	if len(violationsA) == 0 {
		t.Fatal("expected fixture A (cancelAndClose called while mu is locked) to be rejected")
	}

	// Fixture B: calling FinalizeBilling while mu is locked
	badSourceB := `package runtime
func (a *attemptSession) finalize(ctx context.Context) {
	a.mu.Lock()
	a.FinalizeBilling(ctx)
	a.mu.Unlock()
}`
	fsetB := token.NewFileSet()
	fileB, err := parser.ParseFile(fsetB, "attempt_session.go", badSourceB, 0)
	if err != nil {
		t.Fatalf("parse fixture B: %v", err)
	}
	violationsB := inspectLockHeldIOAST(fsetB, fileB, "attempt_session.go")
	if len(violationsB) == 0 {
		t.Fatal("expected fixture B (FinalizeBilling called while mu is locked) to be rejected")
	}

	// Fixture C: calling innerStream.Cancel while mu is locked
	badSourceC := `package runtime
func (a *attemptSession) cancelStream(ctx context.Context) {
	a.mu.Lock()
	a.innerStream.Cancel(ctx, cause)
	a.mu.Unlock()
}`
	fsetC := token.NewFileSet()
	fileC, err := parser.ParseFile(fsetC, "attempt_session.go", badSourceC, 0)
	if err != nil {
		t.Fatalf("parse fixture C: %v", err)
	}
	violationsC := inspectLockHeldIOAST(fsetC, fileC, "attempt_session.go")
	if len(violationsC) == 0 {
		t.Fatal("expected fixture C (innerStream.Cancel called while mu is locked) to be rejected")
	}
}
