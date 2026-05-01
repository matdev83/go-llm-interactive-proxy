package runtimebundle_test

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func testConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "config", "config.yaml")
}

func TestBuildBootstrap_inspectLeavesBuiltNil(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: testConfigPath(t),
		Mode:       runtimebundle.BootstrapInspect,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(context.Background())
		}
	}()
	if res.Built != nil {
		t.Fatal("BootstrapInspect must not call Build; Built must be nil")
	}
	if res.Config == nil || res.Registry == nil || res.App == nil {
		t.Fatalf("expected config, registry, and app: cfg=%v reg=%v app=%v", res.Config != nil, res.Registry != nil, res.App != nil)
	}
}

func TestBuildBootstrap_serveSetsBuiltExecutor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: testConfigPath(t),
		Mode:       runtimebundle.BootstrapServe,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(context.Background())
		}
	}()
	if res.Built == nil || res.Built.Executor == nil {
		t.Fatal("BootstrapServe must produce Built with Executor")
	}
}

func TestBuildBootstrap_nilContext(t *testing.T) {
	t.Parallel()
	_, err := runtimebundle.BuildBootstrap(nil, runtimebundle.BuildBootstrapInput{ //nolint:staticcheck // intentional nil ctx contract
		ConfigPath: testConfigPath(t),
		Mode:       runtimebundle.BootstrapInspect,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildBootstrap_unspecifiedMode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: testConfigPath(t),
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}
