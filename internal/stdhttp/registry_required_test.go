package stdhttp

import (
	"context"
	"net/http"
	"strings"
	"testing"

	coreconfig "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestMountBundledFrontends_nilRegistry(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "ok", nil)
	err := MountBundledFrontends(mux, ex, "stub:x", []coreconfig.PluginConfig{{ID: "openai-responses", Enabled: true}}, 0, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil plugin registry") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRun_nilPluginRegistry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := &coreconfig.Config{
		Server:     coreconfig.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    coreconfig.RoutingConfig{MaxAttempts: 3},
		Continuity: coreconfig.ContinuityConfig{InMemory: true, Store: "memory"},
	}
	app, err := runtime.New(runtime.Options{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	err = Run(ctx, cfg, app, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil plugin registry") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunWithRuntime_nilPluginRegistryInBuilt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := &coreconfig.Config{
		Server:     coreconfig.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    coreconfig.RoutingConfig{MaxAttempts: 3},
		Continuity: coreconfig.ContinuityConfig{InMemory: true, Store: "memory"},
	}
	app, err := runtime.New(runtime.Options{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), nil, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	built.PluginRegistry = nil
	err = RunWithRuntime(ctx, cfg, app, nil, built)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil plugin registry in built runtime") {
		t.Fatalf("unexpected error: %v", err)
	}
}
