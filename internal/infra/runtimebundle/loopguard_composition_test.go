package runtimebundle_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/stretchr/testify/require"
)

func TestLoopGuardWiring_ProductionComposition_EnabledHasGuard(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		AgentLoopGuard: config.AgentLoopGuardConfig{
			Enabled:                  true,
			VerifierRole:             "loop_guard",
			VerifierTimeoutSeconds:   4,
			MaxSemanticContinuations: 3,
			NoProgressLimit:          2,
			ExplicitCompletionPolicy: "trust",
		},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()})
	exec := b.Executor()
	require.NotNil(t, exec.LoopGuardFactory, "production executor with enabled guard must have LoopGuardFactory")
}

func TestLoopGuardWiring_ProductionComposition_DisabledNil(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()})
	exec := b.Executor()
	require.Nil(t, exec.LoopGuardFactory, "production executor with disabled guard must have nil LoopGuardFactory")
}
