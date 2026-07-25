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

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/osenv"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
)

// Test-only stage fault seam for PartialCleanup matrices (req 4.8). Journals are
// evidence from wrapped hostBuildOps; this file must not reimplement production
// NewProcessServices / CompileGeneration / Publish / bindHost bodies.

func (j *hostBuildJournal) acquire(stage string) {
	j.Acquired = append(j.Acquired, stage)
}

func (j *hostBuildJournal) clean(stage string) {
	for _, existing := range j.Cleaned {
		if existing == stage {
			return
		}
	}
	j.Cleaned = append(j.Cleaned, stage)
}

func (productionHostBuilder) BuildFaulting(ctx context.Context, in hostBuildInput, faultAt hostBuildStage) (hostBuildOutcome, error) {
	var journal hostBuildJournal
	ops := defaultHostBuildOps()

	baseLoad := ops.load
	ops.load = func(ctx context.Context, path string, cli config.StreamRecoveryOverrides) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, config.StreamRecoveryOverrides, error) {
		eff, src, fixed, err := baseLoad(ctx, path, cli)
		if err != nil {
			return nil, nil, fixed, err
		}
		journal.acquire("loader")
		journal.Loads++
		if faultAt == hostBuildStageLoader {
			return nil, nil, fixed, fmt.Errorf("runtimebundle: host build fault: loader")
		}
		return eff, src, fixed, nil
	}

	baseTracing := ops.tracing
	ops.tracing = func(ctx context.Context, cfg *config.Config) (tracing.Result, error) {
		res, err := baseTracing(ctx, cfg)
		if err != nil {
			return res, err
		}
		journal.acquire("tracing")
		inner := res.Shutdown
		res.Shutdown = func(ctx context.Context) error {
			journal.clean("tracing")
			if inner == nil {
				return nil
			}
			return inner(ctx)
		}
		if faultAt == hostBuildStageTracing {
			_ = res.Shutdown(context.WithoutCancel(ctx))
			return tracing.Result{}, fmt.Errorf("runtimebundle: host build fault: tracing")
		}
		return res, nil
	}

	baseProcess := ops.process
	ops.process = func(ctx context.Context, in processBuildInput) (*ProcessServices, error) {
		ps, err := baseProcess(ctx, in)
		if err != nil {
			return nil, err
		}
		journal.acquire("process")
		ps.closers = append([]func() error{func() error {
			journal.clean("process")
			return nil
		}}, ps.closers...)
		if faultAt == hostBuildStageProcess {
			_ = ps.Close()
			return nil, fmt.Errorf("runtimebundle: host build fault: process")
		}
		return ps, nil
	}

	baseCompile := ops.compile
	ops.compile = func(ctx context.Context, ps *ProcessServices, cfg *config.Config, compose HandlerComposer) (GenerationRuntime, error) {
		bundle, err := baseCompile(ctx, ps, cfg, compose)
		if err != nil {
			return nil, err
		}
		journal.acquire("compile")
		if faultAt == hostBuildStageCompile {
			journal.clean("compile")
			_ = bundle.Quiesce(context.WithoutCancel(ctx))
			_ = bundle.Close()
			return nil, fmt.Errorf("runtimebundle: host build fault: compile")
		}
		return bundle, nil
	}

	basePublish := ops.publisher
	ops.publisher = func(ctx context.Context, in initialPublishInput) (*runtimehost.Manager, *runtimehost.Generation, error) {
		mgr, gen, err := basePublish(ctx, in)
		if err != nil {
			return nil, nil, err
		}
		journal.acquire("publish")
		if faultAt == hostBuildStagePublish {
			// Match former probe notes before joinInitialFailureCleanup: publish
			// then compile evidence, then production closes process + tracing.
			journal.clean("publish")
			journal.clean("compile")
			return nil, nil, fmt.Errorf("runtimebundle: host build fault: publish")
		}
		return mgr, gen, nil
	}

	baseBind := ops.bind
	ops.bind = func(configPath string, in bindHostInput) (*Host, error) {
		host, err := baseBind(configPath, in)
		if err != nil {
			return nil, err
		}
		journal.acquire("coordinator")
		if faultAt == hostBuildStageCoordinator {
			// Match former probe notes; buildHost closes the returned Host.
			journal.clean("coordinator")
			journal.clean("publish")
			journal.clean("compile")
			journal.clean("process")
			journal.clean("tracing")
			return host, fmt.Errorf("runtimebundle: host build fault: coordinator")
		}
		return host, nil
	}

	host, err := buildHost(ctx, in, ops, osenv.Process{})
	if err != nil {
		return hostBuildOutcome{Journal: journal}, err
	}
	return hostBuildOutcome{Host: host, Journal: journal, Complete: true}, nil
}

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
		t.Fatal("fault seam must stay in package runtimebundle to call buildHost with ops")
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
		"bindHost",
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
	if !strings.Contains(string(src), "defaultHostBuildOps") {
		t.Fatal("BuildFaulting must use the hostBuildOps seam")
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
