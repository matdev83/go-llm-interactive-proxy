package runtimebundle_test

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

func TestBuildHost_UsesStartupFixedStreamRecoverySnapshot(t *testing.T) {
	t.Setenv("LIP_AUTO_RESUME", "true")
	t.Setenv("LIP_AUTO_RESUME_IDLE_TIMEOUT", "12s")

	cfgPath := filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml")
	compose := stdhttp.ComposeStandardHTTP
	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: compose,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })

	if host.FixedStreamRecovery.EnvIdleTimeout != 12*time.Second {
		t.Fatalf("BuildHost snapshot idle=%v want 12s", host.FixedStreamRecovery.EnvIdleTimeout)
	}
	enabled := host.FixedStreamRecovery.EnvEnabled
	if enabled == nil || !*enabled {
		t.Fatalf("BuildHost snapshot enabled=%v want true", enabled)
	}
	if host.Coordinator == nil {
		t.Fatal("nil reload coordinator")
	}

	// Mutate process env after BuildHost. Reload must not reread these; it
	// reuses the startup-fixed snapshot captured exactly once at BuildHost.
	t.Setenv("LIP_AUTO_RESUME", "not-a-bool")
	t.Setenv("LIP_AUTO_RESUME_IDLE_TIMEOUT", "99s")

	// No-op reload still exercises the fixed effective loader with the startup snapshot.
	result := host.Coordinator.Reload(context.Background(), sdkreload.Trigger{
		Kind:       sdkreload.TriggerAPI,
		AcceptedAt: time.Now().UTC(),
		SafeActor:  "test",
	})
	if result.Category == sdkreload.ResultInvalid || result.Category == sdkreload.ResultInternalFailed {
		t.Fatalf("reload failed under mutated env: category=%s reason=%s", result.Category, result.ReasonCategory)
	}

	// Snapshot retained on the Host must remain the startup-fixed values.
	if host.FixedStreamRecovery.EnvIdleTimeout != 12*time.Second {
		t.Fatalf("snapshot mutated: idle=%v", host.FixedStreamRecovery.EnvIdleTimeout)
	}
	// Prove a fresh env read would differ / fail — BuildHost must not have depended on it.
	if _, err := config.StreamRecoveryOverridesFromEnv(); err == nil {
		t.Fatal("expected env parse failure after mutation; test setup broken")
	}
}
