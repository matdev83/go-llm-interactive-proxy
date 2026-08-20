package runtimebundle_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func TestProcessServices_OwnsBranchCoordinatorAcrossGenerationCandidates(t *testing.T) {
	t.Parallel()

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  processServicesCoordinatorConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	if ps.BranchCoordinator == nil {
		t.Fatal("expected process-owned branch coordinator")
	}
	key, err := compactioncontinuity.NewBranchKey("session-parent", "a-parent", "principal-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ps.BranchCoordinator.Capture(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.BranchCoordinator.CommitCapsule(context.Background(), key, 0, []byte(`{"parent":true}`), [32]byte{1}, "source-1"); err != nil {
		t.Fatal(err)
	}
	first := ps.BranchCoordinator
	for range 2 {
		candidate, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
			Process: ps,
			Bus:     hooks.New(hooks.Config{}),
		})
		if err != nil {
			t.Fatal(err)
		}
		_ = candidate.Close()
		if ps.BranchCoordinator != first {
			t.Fatal("candidate compilation/reload must not replace process branch coordinator")
		}
	}
	got, ok, err := ps.BranchCoordinator.Snapshot(context.Background(), key)
	if err != nil || !ok || got.Revision != 1 || string(got.CapsuleJSON) != `{"parent":true}` {
		t.Fatalf("coordinator state = %#v, found=%v, err=%v", got, ok, err)
	}
}

func processServicesCoordinatorConfig() *config.Config {
	return &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 1},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Server:     config.ServerConfig{MaxConcurrentDecodes: 1, MaxInflightDecodeBytes: 1024},
	}
}
