package runtimebundle_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	bpkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestAttachReloadHost_UsesStartupFixedStreamRecoverySnapshot(t *testing.T) {
	t.Setenv("LIP_AUTO_RESUME", "true")
	t.Setenv("LIP_AUTO_RESUME_IDLE_TIMEOUT", "12s")

	cfgPath := bpkit.WriteDogfoodLocalStubConfig(t)
	compose := stdhttp.ComposeRequestPlane
	res, err := runtimebundle.BuildBootstrap(context.Background(), runtimebundle.BuildBootstrapInput{
		ConfigPath:      cfgPath,
		Mode:            runtimebundle.BootstrapServe,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: compose,
	})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(func() {
		if res.GenerationManager != nil {
			_ = res.GenerationManager.ShutdownDetached(context.Background(), runtimehost.NewLifecycleWorker())
		}
		if res.ProcessServices != nil {
			_ = res.ProcessServices.Close()
		}
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(context.Background())
		}
	})

	if res.FixedStreamRecovery.EnvIdleTimeout != 12*time.Second {
		t.Fatalf("bootstrap snapshot idle=%v want 12s", res.FixedStreamRecovery.EnvIdleTimeout)
	}
	enabled := res.FixedStreamRecovery.EnvEnabled
	if enabled == nil || !*enabled {
		t.Fatalf("bootstrap snapshot enabled=%v want true", enabled)
	}

	// Mutate process env after bootstrap. AttachReloadHost must not reread these.
	t.Setenv("LIP_AUTO_RESUME", "not-a-bool")
	t.Setenv("LIP_AUTO_RESUME_IDLE_TIMEOUT", "99s")

	host, err := runtimebundle.AttachReloadHost(context.Background(), res, cfgPath, compose)
	if err != nil {
		t.Fatalf("AttachReloadHost after env mutation: %v", err)
	}
	if host == nil || host.Coordinator == nil {
		t.Fatal("nil reload host")
	}

	// No-op reload still exercises the fixed effective loader with the startup snapshot.
	result := host.Coordinator.Reload(context.Background(), configreload.ReloadTrigger{
		Kind:       configreload.TriggerAPI,
		AcceptedAt: time.Now().UTC(),
		SafeActor:  "test",
	})
	if result.Category == configreload.ResultInvalid || result.Category == configreload.ResultInternalFailed {
		t.Fatalf("reload failed under mutated env: category=%s reason=%s", result.Category, result.ReasonCategory)
	}

	// Snapshot retained on bootstrap result must remain the startup-fixed values.
	if res.FixedStreamRecovery.EnvIdleTimeout != 12*time.Second {
		t.Fatalf("snapshot mutated: idle=%v", res.FixedStreamRecovery.EnvIdleTimeout)
	}
	// Prove a fresh env read would differ / fail — Attach must not have depended on it.
	if _, err := config.StreamRecoveryOverridesFromEnv(); err == nil {
		t.Fatal("expected env parse failure after mutation; test setup broken")
	}
}
