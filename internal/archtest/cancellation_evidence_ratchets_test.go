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

// DetectCurrentSlotEvidenceAttribution scans runtime code to ensure terminal evidence is attributed
// to explicit attempt session references, never by re-reading the mutable current-attempt slot.
func DetectCurrentSlotEvidenceAttribution(root string) ([]string, error) {
	runtimeDir := filepath.Join(root, "internal", "core", "runtime")
	targetFiles := []string{
		"response_pipeline_observations.go",
		"attempt_session.go",
		"executor_recv_loop.go",
	}

	var violations []string
	for _, name := range targetFiles {
		path := filepath.Join(runtimeDir, name)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		violations = append(violations, inspectCurrentSlotEvidenceAttributionAST(fset, file, name)...)
	}
	return violations, nil
}

func inspectCurrentSlotEvidenceAttributionAST(fset *token.FileSet, file *ast.File, relPath string) []string {
	var violations []string

	evidenceFunctions := map[string]bool{
		"prepareRecvEvent":                      true,
		"emitUsage":                             true,
		"emitUsageEvidence":                     true,
		"emitUsageTerminal":                     true,
		"consumeBackendUsageEvidenceForAttempt": true,
		"drainSidebandEvidence":                 true,
		"drainStreamUsageEvidence":              true,
		"makeSwallowedEvidence":                 true,
		"terminalizeSwallowed":                  true,
		"terminalizeEarlyCancellation":          true,
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		if !evidenceFunctions[fn.Name.Name] {
			continue
		}

		// Check that function does not call s.attempt.get(), s.attempt.load(), or req.progress.attempt
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			selText := nodeText(sel)
			if strings.Contains(selText, "s.attempt") || strings.Contains(selText, "progress.attempt") {
				pos := fset.Position(sel.Pos())
				violations = append(violations, fmt.Sprintf("%s:%d: function %s re-reads mutable current-attempt slot (%s) for evidence attribution (must use explicit attempt parameter)", relPath, pos.Line, fn.Name.Name, selText))
			}
			return true
		})
	}

	return violations
}

// DetectSidebandClientEmissionLeak checks that provider sideband evidence is swallowed from client canonical emission.
func DetectSidebandClientEmissionLeak(root string) ([]string, error) {
	runtimeDir := filepath.Join(root, "internal", "core", "runtime")
	path := filepath.Join(runtimeDir, "response_pipeline_observations.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse response_pipeline_observations.go: %w", err)
	}
	return inspectSidebandClientEmissionAST(fset, file, "response_pipeline_observations.go"), nil
}

func inspectSidebandClientEmissionAST(fset *token.FileSet, file *ast.File, relPath string) []string {
	var violations []string

	var hasPrepareRecvSwallowedCheck bool
	var hasTransformSwallowedCheck bool

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		if fn.Name.Name == "prepareRecvEvent" {
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if assign, ok := n.(*ast.AssignStmt); ok {
					for i, lhs := range assign.Lhs {
						if nodeText(lhs) == "prepared.swallowed" && i < len(assign.Rhs) && nodeText(assign.Rhs[i]) == "true" {
							hasPrepareRecvSwallowedCheck = true
						}
					}
				}
				return true
			})
		}

		if fn.Name.Name == "transformClientEvent" {
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if ifStmt, ok := n.(*ast.IfStmt); ok {
					condText := nodeText(ifStmt.Cond)
					if strings.Contains(condText, "out.swallowed") || strings.Contains(condText, "prepared.swallowed") {
						hasTransformSwallowedCheck = true
					}
				}
				return true
			})
		}
	}

	if !hasPrepareRecvSwallowedCheck {
		violations = append(violations, fmt.Sprintf("%s: prepareRecvEvent does not set prepared.swallowed = true for deduplicated / internal sideband evidence", relPath))
	}
	if !hasTransformSwallowedCheck {
		violations = append(violations, fmt.Sprintf("%s: transformClientEvent does not short-circuit when swallowed is true", relPath))
	}

	return violations
}

func TestArch_NoCurrentSlotEvidenceAttribution(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	violations, err := DetectCurrentSlotEvidenceAttribution(root)
	if err != nil {
		t.Fatalf("DetectCurrentSlotEvidenceAttribution failed: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("Current slot evidence attribution ratchet violated (%d violations):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

func TestArch_NoCurrentSlotEvidenceAttribution_NegativeFixtures(t *testing.T) {
	t.Parallel()

	// Fixture A: emitUsage re-reading s.attempt.get() instead of using explicit attempt parameter
	badSourceA := `package runtime
func (p *responsePipeline) emitUsage(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev lipapi.Event) {
	current := s.attempt.get()
	p.emitUsageEvidence(ctx, facts, current, ev)
}`
	fsetA := token.NewFileSet()
	fileA, err := parser.ParseFile(fsetA, "response_pipeline_observations.go", badSourceA, 0)
	if err != nil {
		t.Fatalf("parse fixture A: %v", err)
	}
	violationsA := inspectCurrentSlotEvidenceAttributionAST(fsetA, fileA, "response_pipeline_observations.go")
	if len(violationsA) == 0 {
		t.Fatal("expected fixture A (emitUsage re-reading s.attempt) to be rejected")
	}

	// Fixture B: drainSidebandEvidence re-reading req.progress.attempt
	badSourceB := `package runtime
func (p *responsePipeline) drainSidebandEvidence(ctx context.Context, facts recvTurnFacts, attempt *attemptSession) {
	active := facts.progress.attempt
	_ = active
}`
	fsetB := token.NewFileSet()
	fileB, err := parser.ParseFile(fsetB, "response_pipeline_observations.go", badSourceB, 0)
	if err != nil {
		t.Fatalf("parse fixture B: %v", err)
	}
	violationsB := inspectCurrentSlotEvidenceAttributionAST(fsetB, fileB, "response_pipeline_observations.go")
	if len(violationsB) == 0 {
		t.Fatal("expected fixture B (drainSidebandEvidence re-reading progress.attempt) to be rejected")
	}

	// Fixture C: Valid function using explicit attempt parameter (allowed)
	goodSourceC := `package runtime
func (p *responsePipeline) emitUsage(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev lipapi.Event) {
	p.emitUsageEvidence(ctx, facts, attempt, ev)
}`
	fsetC := token.NewFileSet()
	fileC, err := parser.ParseFile(fsetC, "response_pipeline_observations.go", goodSourceC, 0)
	if err != nil {
		t.Fatalf("parse fixture C: %v", err)
	}
	violationsC := inspectCurrentSlotEvidenceAttributionAST(fsetC, fileC, "response_pipeline_observations.go")
	if len(violationsC) != 0 {
		t.Fatalf("expected valid fixture C to pass, got: %v", violationsC)
	}
}

func TestArch_SidebandEvidenceNotEmittedToClient(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	violations, err := DetectSidebandClientEmissionLeak(root)
	if err != nil {
		t.Fatalf("DetectSidebandClientEmissionLeak failed: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("Sideband client emission ratchet violated (%d violations):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

func TestArch_SidebandEvidenceNotEmittedToClient_NegativeFixtures(t *testing.T) {
	t.Parallel()

	// Fixture A: prepareRecvEvent missing swallowed assignment
	badSourceA := `package runtime
func (p *responsePipeline) prepareRecvEvent(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev lipapi.Event) recvEventPreparation {
	prepared := recvEventPreparation{event: ev}
	return prepared
}
func (p *responsePipeline) transformClientEvent(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev lipapi.Event, prepared recvEventPreparation) clientEventTransformation {
	if out.swallowed { return out }
	return clientEventTransformation{event: ev}
}`
	fsetA := token.NewFileSet()
	fileA, err := parser.ParseFile(fsetA, "response_pipeline_observations.go", badSourceA, 0)
	if err != nil {
		t.Fatalf("parse fixture A: %v", err)
	}
	violationsA := inspectSidebandClientEmissionAST(fsetA, fileA, "response_pipeline_observations.go")
	if len(violationsA) == 0 {
		t.Fatal("expected fixture A (missing swallowed handling in prepareRecvEvent) to be rejected")
	}

	// Fixture B: transformClientEvent missing swallowed check
	badSourceB := `package runtime
func (p *responsePipeline) prepareRecvEvent(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev lipapi.Event) recvEventPreparation {
	prepared := recvEventPreparation{event: ev}
	prepared.swallowed = true
	return prepared
}
func (p *responsePipeline) transformClientEvent(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev lipapi.Event, prepared recvEventPreparation) clientEventTransformation {
	return clientEventTransformation{event: ev}
}`
	fsetB := token.NewFileSet()
	fileB, err := parser.ParseFile(fsetB, "response_pipeline_observations.go", badSourceB, 0)
	if err != nil {
		t.Fatalf("parse fixture B: %v", err)
	}
	violationsB := inspectSidebandClientEmissionAST(fsetB, fileB, "response_pipeline_observations.go")
	if len(violationsB) == 0 {
		t.Fatal("expected fixture B (missing swallowed check in transformClientEvent) to be rejected")
	}

	// Fixture C: Valid implementation with both checks
	goodSourceC := `package runtime
func (p *responsePipeline) prepareRecvEvent(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev lipapi.Event) recvEventPreparation {
	prepared := recvEventPreparation{event: ev}
	prepared.swallowed = true
	return prepared
}
func (p *responsePipeline) transformClientEvent(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev lipapi.Event, prepared recvEventPreparation) clientEventTransformation {
	if out.swallowed { return out }
	return clientEventTransformation{event: ev}
}`
	fsetC := token.NewFileSet()
	fileC, err := parser.ParseFile(fsetC, "response_pipeline_observations.go", goodSourceC, 0)
	if err != nil {
		t.Fatalf("parse fixture C: %v", err)
	}
	violationsC := inspectSidebandClientEmissionAST(fsetC, fileC, "response_pipeline_observations.go")
	if len(violationsC) != 0 {
		t.Fatalf("expected valid fixture C to pass, got: %v", violationsC)
	}
}
