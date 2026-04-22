package runtimebundle_test

import (
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

func TestBuild_sqliteStoreRegistersCloser(t *testing.T) {
	t.Parallel()
	if err := pluginreg.RegisterStandardBundle(); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	dbpath := filepath.Join(tmp, "continuity.db")
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{
			InMemory:   false,
			Store:      "sqlite",
			SQLitePath: dbpath,
		},
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Closers) != 1 {
		t.Fatalf("expected 1 closer for sqlite, got %d", len(b.Closers))
	}
	if err := b.Closers[0](); err != nil {
		t.Fatalf("closer: %v", err)
	}
}
