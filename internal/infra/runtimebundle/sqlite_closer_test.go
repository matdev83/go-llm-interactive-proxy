package runtimebundle_test

import (
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
)

func TestBuild_sqliteStoreRegistersCloser(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
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
	ps, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	// Candidate Closers are generation-owned only; sqlite continuity lives on ProcessServices.
	if len(b.Closers) != 1 {
		t.Fatalf("expected 1 generation closer (upstream idle), got %d", len(b.Closers))
	}
	if ps.Closed() {
		t.Fatal("process must remain open while candidate is live")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("candidate close: %v", err)
	}
	if ps.Closed() {
		t.Fatal("process must remain open after candidate close")
	}
}
