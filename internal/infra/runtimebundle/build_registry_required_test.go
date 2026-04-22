package runtimebundle_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func TestBuild_nilPluginRegistry(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil PluginRegistry") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuild_nilPluginRegistryInOptions(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), nil, &runtimebundle.BuildOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil PluginRegistry") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuild_nilLogger(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), nil, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil logger") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuild_usesProvidedRegistryOnly(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.PluginRegistry != reg {
		t.Fatalf("PluginRegistry pointer: got %p want %p", b.PluginRegistry, reg)
	}
}
