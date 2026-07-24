package runtimebundle

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// Test-only stage fault seam for the ValidateDistribution PartialCleanup matrix
// (req 5.4, 11.7). Journals are evidence translated from the production
// validateDistributionProbe; this file must not reimplement
// LoadBootstrapEffectiveWithSource / NewProcessServices / CompileGeneration /
// GenerationRuntime.Quiesce / GenerationRuntime.Close.

// validateDistributionFaulting invokes the real production validateDistribution
// transaction with a probe that injects a fault at faultAt and journals
// acquire/cleanup evidence, mirroring productionHostBuilder.BuildFaulting.
func validateDistributionFaulting(ctx context.Context, in ValidateDistributionInput, faultAt validateDistributionStage) (validateDistributionJournal, error) {
	var journal validateDistributionJournal
	probe := func(stage validateDistributionStage, event validateDistributionProbeEvent) error {
		switch event {
		case validateProbeAcquired:
			journal.Acquired = append(journal.Acquired, string(stage))
		case validateProbeCleaned:
			journal.Cleaned = append(journal.Cleaned, string(stage))
		}
		if stage == faultAt {
			return fmt.Errorf("runtimebundle: validate distribution fault: %s", stage)
		}
		return nil
	}
	err := validateDistribution(ctx, in, nil, LoadBootstrapEffectiveWithSource, probe)
	return journal, err
}

// TestPartialCleanup_ValidateDistributionFaultSeamUsesProductionTransaction
// guards against a second test-only ownership engine regressing under the
// ValidateDistribution PartialCleanup matrix (mirrors
// TestPartialCleanup_FaultSeamUsesProductionTransaction for BuildHost).
func TestPartialCleanup_ValidateDistributionFaultSeamUsesProductionTransaction(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("validate_distribution_fault_test.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "validate_distribution_fault_test.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if f.Name == nil || f.Name.Name != "runtimebundle" {
		t.Fatal("fault seam must stay in package runtimebundle to call validateDistribution with a probe")
	}
	var faulting *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "validateDistributionFaulting" {
			return true
		}
		faulting = fd
		return false
	})
	if faulting == nil || faulting.Body == nil {
		t.Fatal("missing validateDistributionFaulting")
	}
	forbidden := []string{"NewProcessServices", "CompileGeneration", "installRegistryAndRegistrations", "initProcessTracing"}
	ast.Inspect(faulting.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := callName(call.Fun)
		for _, tok := range forbidden {
			if name == tok {
				t.Fatalf("validateDistributionFaulting must not duplicate production step %s", tok)
			}
		}
		return true
	})
	body := src[fset.Position(faulting.Body.Pos()).Offset:fset.Position(faulting.Body.End()).Offset]
	if !strings.Contains(string(body), "validateDistribution(") {
		t.Fatal("validateDistributionFaulting must invoke production validateDistribution")
	}
	if !strings.Contains(string(src), "validateProbeAcquired") {
		t.Fatal("validateDistributionFaulting must use the production validateDistributionProbe seam")
	}
}
