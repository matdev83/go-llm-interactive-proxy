package runtimebundle_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

func TestBuild_zeroMaxPendingWireEventsRemainsUnlimited(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Server:     config.ServerConfig{}, // MaxPendingWireEvents == 0
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if got := built.Executor().MaxPendingWireEvents; got != 0 {
		t.Fatalf("Executor.MaxPendingWireEvents = %d, want 0 (unlimited)", got)
	}
}
