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

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/contracttest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/contract"
)

// TestArch_CancellationHandshake_ABIPolicy validates the ABI policy for cancellation handshake constants and modes.
func TestArch_CancellationHandshake_ABIPolicy(t *testing.T) {
	t.Parallel()

	// 1. Validate exact minor version and feature name
	if backendplugin.ProtocolMinorCancellationHandshake != 8 {
		t.Fatalf("ProtocolMinorCancellationHandshake = %d, want 8", backendplugin.ProtocolMinorCancellationHandshake)
	}
	if backendplugin.FeatureCancellationHandshake != "cancellation_handshake_v1" {
		t.Fatalf("FeatureCancellationHandshake = %q, want %q", backendplugin.FeatureCancellationHandshake, "cancellation_handshake_v1")
	}

	// 2. Validate ABI mutation policy allows the exact constants
	validMinorSymbol := PublicABISymbol{
		Category: "const",
		Name:     "ProtocolMinorCancellationHandshake",
		Detail:   "ProtocolMinorCancellationHandshake uint32 = 8",
	}
	if err := ValidatePublicBackendPluginABIMutation([]PublicABISymbol{validMinorSymbol}); err != nil {
		t.Fatalf("expected valid ProtocolMinorCancellationHandshake to pass ABI mutation validation: %v", err)
	}

	// 3. Validate ABI mutation policy rejects mutated/near-miss values
	mutatedMinorSymbol := PublicABISymbol{
		Category: "const",
		Name:     "ProtocolMinorCancellationHandshake",
		Detail:   "ProtocolMinorCancellationHandshake uint32 = 99",
	}
	if err := ValidatePublicBackendPluginABIMutation([]PublicABISymbol{mutatedMinorSymbol}); err == nil {
		t.Fatal("expected mutated ProtocolMinorCancellationHandshake to be rejected by ABI mutation validation")
	}

	// 4. Validate known cancellation modes
	validModes := []backendplugin.CancelMode{
		backendplugin.CancelModeNone,
		backendplugin.CancelModeProvider,
		backendplugin.CancelModeTransport,
		backendplugin.CancelModeCloseOnly,
	}
	for _, mode := range validModes {
		if err := backendplugin.ValidateCancelMode(mode); err != nil {
			t.Errorf("ValidateCancelMode(%q) failed: %v", mode, err)
		}
	}
}

// DetectHardcodedProviderCancelMode scans lifecycle and adapter code to ensure no wrapper hardcodes provider mode.
func DetectHardcodedProviderCancelMode(root string) ([]string, error) {
	dirs := []string{
		filepath.Join(root, "internal", "core", "leglifecycle"),
		filepath.Join(root, "internal", "core", "runtime"),
		filepath.Join(root, "internal", "infra", "backendplugins", "adapter"),
	}

	var violations []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("read dir %s: %w", dir, err)
		}
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, ent.Name())
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", ent.Name(), err)
			}
			violations = append(violations, inspectHardcodedProviderCancelModeAST(fset, file, ent.Name())...)
		}
	}
	return violations, nil
}

func inspectHardcodedProviderCancelModeAST(fset *token.FileSet, file *ast.File, relPath string) []string {
	var violations []string

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		// Check if function name is Cancel or returns CancelResult / CancelOutcome
		isCancelFunc := fn.Name.Name == "Cancel" || strings.Contains(fn.Name.Name, "cancel")
		if !isCancelFunc && fn.Type.Results != nil {
			for _, field := range fn.Type.Results.List {
				typStr := nodeText(field.Type)
				if strings.Contains(typStr, "CancelResult") || strings.Contains(typStr, "CancelOutcome") {
					isCancelFunc = true
					break
				}
			}
		}

		if !isCancelFunc {
			continue
		}

		// Inspect return statements inside the function body
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok {
				return true
			}

			for _, expr := range ret.Results {
				retText := nodeText(expr)
				if strings.Contains(retText, "CancelModeProvider") {
					if lit, ok := expr.(*ast.CompositeLit); ok {
						for _, elt := range lit.Elts {
							eltText := nodeText(elt)
							if strings.Contains(eltText, "Mode") && strings.Contains(eltText, "CancelModeProvider") {
								pos := fset.Position(ret.Pos())
								violations = append(violations, fmt.Sprintf("%s:%d: %s hard-codes CancelModeProvider in return: %s", relPath, pos.Line, fn.Name.Name, retText))
							}
						}
					}
				}
			}
			return true
		})
	}

	return violations
}

func TestArch_NoHardcodedProviderCancelMode(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	violations, err := DetectHardcodedProviderCancelMode(root)
	if err != nil {
		t.Fatalf("DetectHardcodedProviderCancelMode failed: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("Hardcoded provider cancel mode ratchet violated (%d violations):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

func TestArch_NoHardcodedProviderCancelMode_NegativeFixtures(t *testing.T) {
	t.Parallel()

	// Fixture A: lifecycle wrapper hardcoding CancelModeProvider in return
	badSourceA := `package leglifecycle
func (h *badHandle) Cancel(ctx context.Context, cause CancelCause) CancelResult {
	return CancelResult{Mode: CancelModeProvider}
}`
	fsetA := token.NewFileSet()
	fileA, err := parser.ParseFile(fsetA, "bad_handle.go", badSourceA, 0)
	if err != nil {
		t.Fatalf("parse fixture A: %v", err)
	}
	violationsA := inspectHardcodedProviderCancelModeAST(fsetA, fileA, "bad_handle.go")
	if len(violationsA) == 0 {
		t.Fatal("expected fixture A (hardcoded CancelModeProvider) to be rejected")
	}

	// Fixture B: adapter hardcoding CancelOutcome with CancelModeProvider
	badSourceB := `package adapter
func (s *managedStream) cancelAttempt(ctx context.Context) CancelOutcome {
	return CancelOutcome{Mode: CancelModeProvider, Acknowledged: true}
}`
	fsetB := token.NewFileSet()
	fileB, err := parser.ParseFile(fsetB, "bad_adapter.go", badSourceB, 0)
	if err != nil {
		t.Fatalf("parse fixture B: %v", err)
	}
	violationsB := inspectHardcodedProviderCancelModeAST(fsetB, fileB, "bad_adapter.go")
	if len(violationsB) == 0 {
		t.Fatal("expected fixture B (hardcoded CancelOutcome with CancelModeProvider) to be rejected")
	}

	// Fixture C: CloseOnly wrapper returning CancelModeCloseOnly (allowed)
	goodSourceC := `package leglifecycle
func (h *closeOnlyHandle) Cancel(ctx context.Context, cause CancelCause) CancelResult {
	return CancelResult{Mode: CancelModeCloseOnly}
}`
	fsetC := token.NewFileSet()
	fileC, err := parser.ParseFile(fsetC, "good_handle.go", goodSourceC, 0)
	if err != nil {
		t.Fatalf("parse fixture C: %v", err)
	}
	violationsC := inspectHardcodedProviderCancelModeAST(fsetC, fileC, "good_handle.go")
	if len(violationsC) != 0 {
		t.Fatalf("expected valid CloseOnly fixture C to pass, got: %v", violationsC)
	}
}

func TestArch_ContractTCK_ActiveCancellationCertified(t *testing.T) {
	t.Parallel()

	corpus := contract.BaselineScenarioCorpus()
	var foundCancellation bool
	for _, sc := range corpus {
		if sc.Feature == contract.FeatureCancellation {
			foundCancellation = true
			break
		}
	}
	if !foundCancellation {
		t.Fatal("contract.BaselineScenarioCorpus() must include FeatureCancellation scenario")
	}

	result := contracttest.CertificationResult{
		PluginID: "test-plugin",
		Version:  "1.0.0",
		Negotiated: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
		ScenarioResults: make([]contracttest.ScenarioResult, len(corpus)),
	}
	for i, sc := range corpus {
		result.ScenarioResults[i] = contracttest.ScenarioResult{
			ID:              string(sc.ID),
			Positive:        true,
			Executed:        true,
			FramesValidated: 1,
			Terminal:        true,
			Cancelled:       sc.Feature == contract.FeatureCancellation,
		}
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("expected valid certification result to pass: %v", err)
	}
}
