package runtimebundle_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func TestBuild_zeroMaxPendingWireEventsRemainsUnlimited(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Server:     config.ServerConfig{}, // MaxPendingWireEvents == 0
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, c := range built.Closers {
			_ = c()
		}
	})
	if got := built.Executor.MaxPendingWireEvents; got != 0 {
		t.Fatalf("Executor.MaxPendingWireEvents = %d, want 0 (unlimited)", got)
	}
}
