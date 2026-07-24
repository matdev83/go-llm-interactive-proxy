package runtimebundle

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// TestBuildHost_CLIMultiUserGateRejectsBeforeTracing proves the serve-only CLI
// gate runs after the one accepted load and before tracing/process acquisition.
func TestBuildHost_CLIMultiUserGateRejectsBeforeTracing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := writeOneSnapshotMarkerConfig(t, "127.0.0.1:18401", accessmode.ModeMultiUser)

	var acquired []string
	probe := func(stage hostBuildStageName, event hostBuildProbeEvent) error {
		if event == hostBuildProbeAcquired {
			acquired = append(acquired, string(stage))
		}
		return nil
	}
	host, err := buildHost(ctx, hostBuildInput{
		ConfigPath:              path,
		Mandatory:               lipsdk.StandardDistributionRequirements(),
		LogWriter:               io.Discard,
		HandlerComposer:         stubHandlerComposer,
		EnforceMultiUserCLIGate: true,
		MultiUser:               nil,
	}, LoadBootstrapEffectiveWithSource, probe)
	if host != nil {
		t.Cleanup(func() { cleanupReloadHost(t, host) })
		t.Fatal("CLI gate failure must return nil Host")
	}
	if !errors.Is(err, accessmode.ErrMultiUserFlagRequired) {
		t.Fatalf("want ErrMultiUserFlagRequired, got %v", err)
	}
	if len(acquired) != 1 || acquired[0] != string(hostBuildStageNameLoader) {
		t.Fatalf("gate must run after loader and before tracing; acquired=%v", acquired)
	}
}

// TestBuildHost_PublicPathSkipsCLIMultiUserGate proves EnforceMultiUserCLIGate=false
// accepts a valid multi_user config without a serve CLI flag (req 4.3).
func TestBuildHost_PublicPathSkipsCLIMultiUserGate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := writeOneSnapshotMarkerConfig(t, "127.0.0.1:18402", accessmode.ModeMultiUser)
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
	t.Cleanup(func() { cleanupReloadHost(t, host) })
	if host.Manager == nil || host.Manager.Active() == nil {
		t.Fatal("expected complete Host")
	}
}

// TestHostClose_TracingShutdownExactlyOnceOnSuccess keeps Host.Close tracing
// ownership retryable on failure and exactly-once after success.
func TestHostClose_TracingShutdownExactlyOnceOnSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfgPath := filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml")
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
	host.ShutdownTracing = func(context.Context) error {
		calls++
		return nil
	}
	if err := host.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if calls != 1 {
		t.Fatalf("tracing calls=%d want 1", calls)
	}
	if host.ShutdownTracing != nil {
		t.Fatal("successful Close must clear ShutdownTracing")
	}
	if err := host.Close(ctx); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
	if calls != 1 {
		t.Fatalf("idempotent Close must not re-invoke tracing: calls=%d", calls)
	}
}
