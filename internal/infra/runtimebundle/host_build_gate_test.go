package runtimebundle

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/osenv"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	bpkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func gateConfigPath(t *testing.T, address string, mode accessmode.Mode) string {
	t.Helper()
	return bpkit.MaterializeExampleConfig(t, writeOneSnapshotMarkerConfig(t, address, mode))
}

// TestBuildHost_CLIMultiUserGateRejectsBeforeTracing proves the serve-only CLI
// gate runs after the one accepted load and before tracing/process acquisition.
func TestBuildHost_CLIMultiUserGateRejectsBeforeTracing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := gateConfigPath(t, "127.0.0.1:18401", accessmode.ModeMultiUser)

	var acquired []string
	ops := gateOpsRejectingAfterLoad(t, &acquired)
	host, err := buildHost(ctx, hostBuildInput{
		ConfigPath:              path,
		Mandatory:               lipsdk.StandardDistributionRequirements(),
		LogWriter:               io.Discard,
		HandlerComposer:         stubHandlerComposer,
		EnforceMultiUserCLIGate: true,
		MultiUser:               nil,
	}, ops, osenv.Process{})
	if host != nil {
		t.Cleanup(func() { cleanupHost(t, host) })
		t.Fatal("CLI gate failure must return nil Host")
	}
	if !errors.Is(err, accessmode.ErrMultiUserFlagRequired) {
		t.Fatalf("want ErrMultiUserFlagRequired, got %v", err)
	}
	if len(acquired) != 1 || acquired[0] != "loader" {
		t.Fatalf("gate must run after loader and before tracing; acquired=%v", acquired)
	}
}

// TestBuildHost_CLIMultiUserGateRejectsExplicitFalse covers multi_user config
// with MultiUser=false (same ErrMultiUserFlagRequired as a nil flag).
func TestBuildHost_CLIMultiUserGateRejectsExplicitFalse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := gateConfigPath(t, "127.0.0.1:18403", accessmode.ModeMultiUser)
	flagFalse := false
	var acquired []string
	ops := gateOpsRejectingAfterLoad(t, &acquired)
	host, err := buildHost(ctx, hostBuildInput{
		ConfigPath:              path,
		Mandatory:               lipsdk.StandardDistributionRequirements(),
		LogWriter:               io.Discard,
		HandlerComposer:         stubHandlerComposer,
		EnforceMultiUserCLIGate: true,
		MultiUser:               &flagFalse,
	}, ops, osenv.Process{})
	if host != nil {
		t.Cleanup(func() { cleanupHost(t, host) })
		t.Fatal("explicit false must return nil Host")
	}
	if !errors.Is(err, accessmode.ErrMultiUserFlagRequired) {
		t.Fatalf("want ErrMultiUserFlagRequired, got %v", err)
	}
	if len(acquired) != 1 || acquired[0] != "loader" {
		t.Fatalf("gate must run after one load and before tracing; acquired=%v", acquired)
	}
}

// TestBuildHost_CLIMultiUserGateRejectsInconsistentTrue covers single_user
// config with MultiUser=true (ErrMultiUserFlagInconsistent).
func TestBuildHost_CLIMultiUserGateRejectsInconsistentTrue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := gateConfigPath(t, "127.0.0.1:18404", accessmode.ModeSingleUser)
	flagTrue := true
	var acquired []string
	ops := gateOpsRejectingAfterLoad(t, &acquired)
	host, err := buildHost(ctx, hostBuildInput{
		ConfigPath:              path,
		Mandatory:               lipsdk.StandardDistributionRequirements(),
		LogWriter:               io.Discard,
		HandlerComposer:         stubHandlerComposer,
		EnforceMultiUserCLIGate: true,
		MultiUser:               &flagTrue,
	}, ops, osenv.Process{})
	if host != nil {
		t.Cleanup(func() { cleanupHost(t, host) })
		t.Fatal("inconsistent true must return nil Host")
	}
	if !errors.Is(err, accessmode.ErrMultiUserFlagInconsistent) {
		t.Fatalf("want ErrMultiUserFlagInconsistent, got %v", err)
	}
	if len(acquired) != 1 || acquired[0] != "loader" {
		t.Fatalf("gate must run after one load and before tracing; acquired=%v", acquired)
	}
}

func gateOpsRejectingAfterLoad(t *testing.T, acquired *[]string) hostBuildOps {
	t.Helper()
	ops := defaultHostBuildOps()
	baseLoad := ops.load
	ops.load = func(ctx context.Context, path string, cli config.StreamRecoveryOverrides) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, config.StreamRecoveryOverrides, error) {
		eff, src, fixed, err := baseLoad(ctx, path, cli)
		if err != nil {
			return nil, nil, fixed, err
		}
		*acquired = append(*acquired, "loader")
		return eff, src, fixed, nil
	}
	ops.tracing = func(context.Context, *config.Config) (tracing.Result, error) {
		t.Fatal("tracing must not run after CLI gate rejection")
		return tracing.Result{}, nil
	}
	return ops
}

// TestBuildHost_PublicPathSkipsCLIMultiUserGate proves EnforceMultiUserCLIGate=false
// accepts a valid multi_user config without a serve CLI flag (req 4.3).
func TestBuildHost_PublicPathSkipsCLIMultiUserGate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := gateConfigPath(t, "127.0.0.1:18402", accessmode.ModeMultiUser)
	host, err := BuildHost(ctx, BuildHostInput{
		ConfigPath:      path,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
		// EnforceMultiUserCLIGate left false: public Build shape.
	})
	if err != nil {
		t.Fatalf("public BuildHost must not require --multi-user: %v", err)
	}
	t.Cleanup(func() { cleanupHost(t, host) })
	if host.manager == nil || host.manager.Active() == nil {
		t.Fatal("expected complete Host")
	}
}

// TestHostClose_TracingShutdownExactlyOnceOnSuccess keeps Host.Close tracing
// ownership retryable on failure and exactly-once after success.
func TestHostClose_TracingShutdownExactlyOnceOnSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfgPath := bpkit.MaterializeExampleConfig(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	host, err := BuildHost(ctx, BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	calls := 0
	host.shutdownTracing = func(context.Context) error {
		calls++
		return nil
	}
	if err := host.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if calls != 1 {
		t.Fatalf("tracing calls=%d want 1", calls)
	}
	if host.shutdownTracing != nil {
		t.Fatal("successful Close must clear ShutdownTracing")
	}
	if err := host.Close(ctx); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
	if calls != 1 {
		t.Fatalf("idempotent Close must not re-invoke tracing: calls=%d", calls)
	}
}
