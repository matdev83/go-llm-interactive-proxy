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

func TestMountBundledFrontends_nilMux(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "ok", nil)
	reg := pluginreg.NewRegistry()
	err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux:                  nil,
		Exec:                 ex,
		DefaultRouteSelector: "stub:x",
		Plugins:              []coreconfig.PluginConfig{{ID: "openai-responses", Enabled: true}},
		MaxRequestBodyBytes:  0,
		Reg:                  reg,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil mux") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMountBundledFrontends_nilExec(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	reg := pluginreg.NewRegistry()
	err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux:                  mux,
		Exec:                 nil,
		DefaultRouteSelector: "stub:x",
		Plugins:              []coreconfig.PluginConfig{{ID: "openai-responses", Enabled: true}},
		MaxRequestBodyBytes:  0,
		Reg:                  reg,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil exec") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMountBundledFrontends_nilRegistry(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "ok", nil)
	err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux:                  mux,
		Exec:                 ex,
		DefaultRouteSelector: "stub:x",
		Plugins:              []coreconfig.PluginConfig{{ID: "openai-responses", Enabled: true}},
		MaxRequestBodyBytes:  0,
		Reg:                  nil,
	})
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
	app, err := runtime.New(runtime.Options{Config: cfg, Logger: testkit.DiscardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	err = Run(ctx, cfg, app, testkit.DiscardLogger(), nil)
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
	log := testkit.DiscardLogger()
	app, err := runtime.New(runtime.Options{Config: cfg, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), log, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	built.PluginRegistry = nil
	err = RunWithRuntime(ctx, cfg, app, log, built)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil plugin registry in built runtime") {
		t.Fatalf("unexpected error: %v", err)
	}
}
