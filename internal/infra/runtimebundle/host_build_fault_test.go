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

// Test-only stage fault seam for PartialCleanup matrices (req 4.8). Journals are
// evidence translated from the production hostBuildProbe; this file must not
// reimplement NewProcessServices / CompileGeneration / Publish / bindReloadHost.

func (productionHostBuilder) BuildFaulting(ctx context.Context, in hostBuildInput, faultAt hostBuildStage) (hostBuildOutcome, error) {
	var journal hostBuildJournal
	probe := func(stage hostBuildStageName, event hostBuildProbeEvent) error {
		switch event {
		case hostBuildProbeAcquired:
			journal.Acquired = append(journal.Acquired, string(stage))
			journal.Loads = countLoaderAcquires(journal.Acquired)
			if hostBuildStage(stage) == faultAt {
				return fmt.Errorf("runtimebundle: host build fault: %s", stage)
			}
		case hostBuildProbeCleaned:
			journal.Cleaned = append(journal.Cleaned, string(stage))
		}
		return nil
	}
	host, err := buildHost(ctx, in, LoadBootstrapEffectiveWithSource, probe)
	if err != nil {
		return hostBuildOutcome{Journal: journal}, err
	}
	return hostBuildOutcome{Host: host, Journal: journal, Complete: true}, nil
}

func countLoaderAcquires(acquired []string) int {
	n := 0
	for _, s := range acquired {
		if s == string(hostBuildStageNameLoader) {
			n++
		}
	}
	return n
}

// TestPartialCleanup_FaultSeamUsesProductionTransaction guards against a
// second test-only ownership engine regressing under the PartialCleanup matrix.
func TestPartialCleanup_FaultSeamUsesProductionTransaction(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("host_build_fault_test.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "host_build_fault_test.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if f.Name == nil || f.Name.Name != "runtimebundle" {
		t.Fatal("fault seam must stay in package runtimebundle to call buildHost with probe")
	}
	var buildFaulting *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Recv == nil || len(fd.Recv.List) == 0 {
			return true
		}
		if fd.Name.Name != "BuildFaulting" {
			return true
		}
		buildFaulting = fd
		return false
	})
	if buildFaulting == nil || buildFaulting.Body == nil {
		t.Fatal("missing productionHostBuilder.BuildFaulting")
	}
	forbidden := []string{
		"NewProcessServices",
		"CompileGeneration",
		"Publish",
		"bindReloadHost",
		"initProcessTracing",
		"installRegistryAndRegistrations",
	}
	ast.Inspect(buildFaulting.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			name := callName(x.Fun)
			for _, tok := range forbidden {
				if name == tok {
					t.Fatalf("BuildFaulting must not duplicate production step %s", tok)
				}
			}
		}
		return true
	})
	body := src[fset.Position(buildFaulting.Body.Pos()).Offset:fset.Position(buildFaulting.Body.End()).Offset]
	if !strings.Contains(string(body), "buildHost(") {
		t.Fatal("BuildFaulting must invoke production buildHost")
	}
	if !strings.Contains(string(src), "hostBuildProbeAcquired") {
		t.Fatal("BuildFaulting must use the production hostBuildProbe seam")
	}
}

func callName(fun ast.Expr) string {
	switch x := fun.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		if x.Sel != nil {
			return x.Sel.Name
		}
	}
	return ""
}
