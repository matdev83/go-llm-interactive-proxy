package runtimebundle_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/modelcatalog/modelsdev"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"gopkg.in/yaml.v3"
)

func TestBuild_modelCatalog_disabled_noRuntime(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBackendsOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{Kind: "openai-responses", ID: "openai-only", Enabled: true, Config: empty},
			},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		ModelCatalog: config.ModelCatalogConfig{
			Enabled:                false,
			ExternalUpdatesEnabled: false,
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.CatalogRuntime != nil {
		t.Fatalf("expected nil CatalogRuntime when catalog disabled")
	}
	if b.Executor.CatalogResolver != nil {
		t.Fatalf("expected nil CatalogResolver")
	}
	if b.Executor.RequestTokenEstimator != nil {
		t.Fatalf("expected nil RequestTokenEstimator")
	}
}

func TestBuild_modelCatalog_enabled_wiresResolversEstimatorAndCloser(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBackendsOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "catalog.json")
	raw := []byte(`{"p":{"id":"p","models":[{"id":"m","tool_call":true}]}}`)
	s0, err := modelsdev.ParseSnapshot(raw, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := modelsdev.NewFileSnapshotStore(cachePath).Save(context.Background(), s0); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{Kind: "openai-responses", ID: "openai-only", Enabled: true, Config: empty},
			},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		ModelCatalog: config.ModelCatalogConfig{
			Enabled:                true,
			ExternalUpdatesEnabled: false,
			CachePath:              cachePath,
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.CatalogRuntime == nil {
		t.Fatal("expected CatalogRuntime")
	}
	if b.Executor.CatalogResolver == nil || b.Executor.EligibilityResolver == nil || b.Executor.RequestTokenEstimator == nil {
		t.Fatalf("expected catalog wiring on executor: cr=%v el=%v rte=%v",
			b.Executor.CatalogResolver != nil,
			b.Executor.EligibilityResolver != nil,
			b.Executor.RequestTokenEstimator != nil)
	}
	if len(b.Closers) == 0 {
		t.Fatal("expected closers")
	}
	for i := len(b.Closers) - 1; i >= 0; i-- {
		if err := b.Closers[i](); err != nil {
			t.Fatalf("closer %d: %v", i, err)
		}
	}
}

func TestBuild_modelCatalog_enabled_missingCachePath_validationFails(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBackendsOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{Kind: "openai-responses", ID: "openai-only", Enabled: true, Config: empty},
			},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		ModelCatalog: config.ModelCatalogConfig{
			Enabled:   true,
			CachePath: "",
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "model_catalog.cache_path") {
		t.Fatalf("want cache_path validation error, got %v", err)
	}
}
