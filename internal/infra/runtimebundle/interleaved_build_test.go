package runtimebundle_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

func interleavedBuildTestRegistry(t *testing.T) *pluginreg.Registry {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("stub", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"stub"},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func interleavedBuildTestConfig(t *testing.T, interleaved config.InterleavedConfig, configDir string) *config.Config {
	t.Helper()
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			{Kind: "stub", ID: "stub", Enabled: true, Config: empty},
		}},
		Interleaved: interleaved,
		ConfigDir:   configDir,
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestBuild_interleavedDisabled_leavesExecutorInert(t *testing.T) {
	t.Parallel()
	cfg := interleavedBuildTestConfig(t, config.InterleavedConfig{Enabled: false}, "")
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: interleavedBuildTestRegistry(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if built.Executor.MemoStore != nil {
		t.Fatal("disabled interleaved must not wire MemoStore")
	}
	if strings.TrimSpace(built.Executor.InterleavedConfig.Instructions) != "" {
		t.Fatalf("disabled interleaved must not wire instructions, got %q", built.Executor.InterleavedConfig.Instructions)
	}
}

func TestBuild_interleavedEnabled_wiresExecutor(t *testing.T) {
	t.Parallel()
	cfg := interleavedBuildTestConfig(t, config.InterleavedConfig{
		Enabled:               true,
		StreamToClient:        "visible",
		RegularTurnsRemaining: 5,
		MaxMemoBytes:          4096,
	}, "")
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: interleavedBuildTestRegistry(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if built.Executor.MemoStore == nil {
		t.Fatal("enabled interleaved must wire MemoStore")
	}
	if strings.TrimSpace(built.Executor.InterleavedConfig.Instructions) == "" {
		t.Fatal("enabled interleaved must wire default instructions")
	}
	if got := built.Executor.InterleavedConfig.StreamToClient; got != "visible" {
		t.Fatalf("StreamToClient = %q, want visible", got)
	}
	if got := built.Executor.InterleavedConfig.RegularTurnsRemaining; got != 5 {
		t.Fatalf("RegularTurnsRemaining = %d, want 5", got)
	}
	if got := built.Executor.InterleavedConfig.MaxMemoBytes; got != 4096 {
		t.Fatalf("MaxMemoBytes = %d, want 4096", got)
	}
}

func TestBuild_interleavedEnabled_loadsInstructionsFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "thinker.md")
	const want = "Custom thinker instructions for wiring test."
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := interleavedBuildTestConfig(t, config.InterleavedConfig{
		Enabled:          true,
		InstructionsFile: "thinker.md",
	}, dir)
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: interleavedBuildTestRegistry(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(built.Executor.InterleavedConfig.Instructions); got != want {
		t.Fatalf("Instructions = %q, want %q", got, want)
	}
}

func TestBuild_interleavedEnabled_missingInstructionsFileFails(t *testing.T) {
	t.Parallel()
	cfg := interleavedBuildTestConfig(t, config.InterleavedConfig{
		Enabled:          true,
		InstructionsFile: filepath.Join("missing.md"),
	}, t.TempDir())
	_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: interleavedBuildTestRegistry(t),
	})
	if err == nil {
		t.Fatal("expected build failure for missing instructions file")
	}
	if !strings.Contains(err.Error(), "instructions_file") {
		t.Fatalf("error %q must mention instructions_file", err.Error())
	}
}
