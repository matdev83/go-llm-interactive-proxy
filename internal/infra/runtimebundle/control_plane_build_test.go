package runtimebundle_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func controlPlaneBuildConfig() *config.Config {
	return &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
}

func buildControlPlaneBundle(t *testing.T, cfg *config.Config) *runtimebundle.Built {
	t.Helper()
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Dispose every Build-owned resource in reverse registration order so a
	// t.Fatalf in any caller still releases handles (e.g. sqlite file locks
	// that would block t.TempDir cleanup on Windows). Matches disposeClosers
	// in build.go.
	t.Cleanup(func() {
		for i := len(built.Closers) - 1; i >= 0; i-- {
			_ = built.Closers[i]()
		}
	})
	return built
}

func TestBuild_ControlPlaneDisabled_DefaultNoHandles(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneBuildConfig()
	cfg.ControlPlane.Enabled = false
	built := buildControlPlaneBundle(t, cfg)
	if built.ControlPlaneQueries != nil {
		t.Fatalf("disabled: expected nil ControlPlaneQueries")
	}
	if built.ControlPlaneStatus != nil {
		t.Fatalf("disabled: expected nil ControlPlaneStatus")
	}
	if built.ControlPlaneRetention != nil {
		t.Fatalf("disabled: expected nil ControlPlaneRetention")
	}
}

func TestBuild_ControlPlaneMemory_WiresStatusNotQueries(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneBuildConfig()
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "memory"
	cfg.ControlPlane.RecordingPolicy = "best_effort"
	built := buildControlPlaneBundle(t, cfg)
	if built.ControlPlaneStatus == nil {
		t.Fatalf("memory: expected ControlPlaneStatus")
	}
	if built.ControlPlaneQueries != nil {
		t.Fatalf("memory: query disabled, expected nil ControlPlaneQueries")
	}
}

func TestBuild_ControlPlaneQueryEnabled_WiresQueries(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneBuildConfig()
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "memory"
	cfg.ControlPlane.Query.Enabled = true
	cfg.ControlPlane.Query.PathPrefix = "/cp"
	cfg.Diagnostics.SharedSecret = "test-secret-1234"
	built := buildControlPlaneBundle(t, cfg)
	if built.ControlPlaneQueries == nil {
		t.Fatalf("query: expected ControlPlaneQueries")
	}
	if built.ControlPlaneStatus == nil {
		t.Fatalf("query: expected ControlPlaneStatus")
	}
}

func TestBuild_ControlPlaneSqlite_WiresStatusAndCloser(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneBuildConfig()
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "sqlite"
	cfg.ControlPlane.SQLitePath = t.TempDir() + "/cp.sqlite"
	built := buildControlPlaneBundle(t, cfg)
	if built.ControlPlaneStatus == nil {
		t.Fatalf("sqlite: expected ControlPlaneStatus")
	}
	// closers disposed via buildControlPlaneBundle t.Cleanup
}
