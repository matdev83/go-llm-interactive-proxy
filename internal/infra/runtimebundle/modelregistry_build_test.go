package runtimebundle_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	refvllm "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/vllm"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func TestBuild_requiresModelInventoryForEnabledBackends(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-no-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-no-inventory"},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	_, err := runtimebundle.Build(modelRegistryTestConfig("test-no-inventory"), hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if !errors.Is(err, modelregistry.ErrMissingProvider) {
		t.Fatalf("Build() error = %v, want ErrMissingProvider", err)
	}
}

func TestBuild_exposesModelRegistryForFastLookup(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-inventory"},
			ModelInventory: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"},
			}},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	b, err := runtimebundle.Build(modelRegistryTestConfig("test-inventory"), hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.ModelRegistry == nil {
		t.Fatal("ModelRegistry is nil")
	}
	got, ok := b.ModelRegistry.Lookup("openai/gpt-4o")
	if !ok {
		t.Fatal("Lookup(openai/gpt-4o) ok = false")
	}
	if len(got) != 1 || got[0].BackendID != "test-backend" || got[0].NativeID != "gpt-4o" {
		t.Fatalf("Lookup(openai/gpt-4o) = %+v", got)
	}
}

func TestBuild_vllmBackendDiscoversModelsIntoRegistry(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(refvllm.NewHandler(refvllm.Config{}))
	t.Cleanup(srv.Close)

	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBackendsOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := modelRegistryTestConfig("vllm")
	cfg.Routing.DefaultRoute = "vllm:meta-llama/Llama-3-8B-Instruct"
	cfg.Plugins.Backends[0].Config = mustYAMLNode(t, `base_url: `+srv.URL+`/v1
api_key: vllm-test
discovery:
  catalog: false
`)

	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeModelRegistryBuilt(t, b))

	got, ok := b.ModelRegistry.Lookup("meta-llama/Llama-3-8B-Instruct")
	if !ok {
		t.Fatal("Lookup(meta-llama/Llama-3-8B-Instruct) ok = false")
	}
	if len(got) != 1 || got[0].BackendID != "test-backend" || got[0].Kind != "vllm" || got[0].NativeID != "meta-llama/Llama-3-8B-Instruct" {
		t.Fatalf("Lookup(meta-llama/Llama-3-8B-Instruct) = %+v", got)
	}
}

func TestBuild_modelRegistryLoadsCacheWithoutRemoteInventoryCall(t *testing.T) {
	t.Parallel()

	cachePath := filepath.Join(t.TempDir(), "backend-models.json")
	writeModelRegistryCache(t, cachePath, modelregistry.Snapshot{
		Generation:  "cached",
		RefreshedAt: time.Unix(100, 0).UTC(),
		Models: []modelregistry.BackendModel{{
			CanonicalID: "openai/gpt-cached",
			NativeID:    "gpt-cached",
			BackendID:   "test-backend",
			Kind:        "test-inventory",
			Source:      modelinventory.SourceRemote,
			LoadedAt:    time.Unix(100, 0).UTC(),
		}},
	})
	provider := &runtimeBundleCountingInventory{err: errors.New("remote must not be called")}
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-inventory"},
			ModelInventory:  provider,
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	cfg := modelRegistryTestConfig("test-inventory")
	cfg.ModelInventory.CachePath = cachePath
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeModelRegistryBuilt(t, b))

	if provider.Calls() != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.Calls())
	}
	got, ok := b.ModelRegistry.Lookup("openai/gpt-cached")
	if !ok || len(got) != 1 || got[0].BackendID != "test-backend" {
		t.Fatalf("cached lookup = %+v, %v", got, ok)
	}
	if b.ModelRegistryRuntime == nil {
		t.Fatal("ModelRegistryRuntime is nil")
	}
}

func TestBuild_modelRegistryColdStartSavesCache(t *testing.T) {
	t.Parallel()

	cachePath := filepath.Join(t.TempDir(), "backend-models.json")
	provider := &runtimeBundleCountingInventory{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-remote",
		NativeID:    "gpt-remote",
	}}}
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-inventory"},
			ModelInventory:  provider,
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	cfg := modelRegistryTestConfig("test-inventory")
	cfg.ModelInventory.CachePath = cachePath
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeModelRegistryBuilt(t, b))

	if provider.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.Calls())
	}
	saved := readModelRegistryCache(t, cachePath)
	if len(saved.Models) != 1 || saved.Models[0].CanonicalID != "openai/gpt-remote" {
		t.Fatalf("saved cache = %+v", saved)
	}
}

func TestBuild_modelRegistryColdStartFailsWhenCacheAndRemoteUnavailable(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-inventory"},
			ModelInventory:  modelinventory.ErrorProvider{Err: errors.New("remote unavailable")},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	cfg := modelRegistryTestConfig("test-inventory")
	cfg.ModelInventory.CachePath = filepath.Join(t.TempDir(), "missing.json")
	_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err == nil || !strings.Contains(err.Error(), "model registry") {
		t.Fatalf("Build() error = %v, want model registry failure", err)
	}
}

func TestBuild_modelRegistryStaticInventoryDoesNotStartRefreshCloser(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-inventory"},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	b, err := runtimebundle.Build(modelRegistryTestConfig("test-inventory"), hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Closers) != 0 {
		t.Fatalf("closers = %d, want 0 for disabled model catalog with static inventory", len(b.Closers))
	}
	closeRuntimeBuilt(t, b)
}

func TestBuild_modelRegistryErrorProviderWithCacheDoesNotStartRefreshCloser(t *testing.T) {
	t.Parallel()

	cachePath := filepath.Join(t.TempDir(), "backend-models.json")
	writeModelRegistryCache(t, cachePath, modelregistry.Snapshot{
		Generation:  "cached",
		RefreshedAt: time.Unix(100, 0).UTC(),
		Models: []modelregistry.BackendModel{{
			CanonicalID: "openai/gpt-cached",
			NativeID:    "gpt-cached",
			BackendID:   "test-backend",
			Kind:        "test-error-inventory",
			Source:      modelinventory.SourceRemote,
			LoadedAt:    time.Unix(100, 0).UTC(),
		}},
	})

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-error-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-error-inventory"},
			ModelInventory:  modelinventory.ErrorProvider{Err: errors.New("backend construction failed")},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	cfg := modelRegistryTestConfig("test-error-inventory")
	cfg.ModelInventory.CachePath = cachePath
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Closers) != 0 {
		t.Fatalf("closers = %d, want 0 for disabled model catalog with cached model registry", len(b.Closers))
	}
	defer closeRuntimeBuilt(t, b)
	if b.ModelRegistryRuntime == nil {
		t.Fatal("ModelRegistryRuntime is nil")
	}
	if b.ModelRegistryRuntime.LastRefreshFailure() != modelregistry.RefreshFailureNone {
		t.Fatalf("LastRefreshFailure = %q, want none", b.ModelRegistryRuntime.LastRefreshFailure())
	}
	got, ok := b.ModelRegistry.Lookup("openai/gpt-cached")
	if !ok || len(got) != 1 || got[0].BackendID != "test-backend" {
		t.Fatalf("cached lookup = %+v, %v", got, ok)
	}
}

func TestBuild_modelRegistryFetchTimeoutAppliesPerBackend(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-inventory-a", delayedBackendFactory("test-inventory-a", "vendor/a", 75*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterBackend("test-inventory-b", delayedBackendFactory("test-inventory-b", "vendor/b", 75*time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	cfg := modelRegistryTestConfig("test-inventory-a")
	cfg.ModelInventory.FetchTimeout = "100ms"
	cfg.Plugins.Backends = append(cfg.Plugins.Backends, config.PluginConfig{
		Kind:    "test-inventory-b",
		ID:      "test-backend-b",
		Enabled: true,
	})

	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeModelRegistryBuilt(t, b))
	if got := b.ModelRegistry.All(); len(got) != 2 {
		t.Fatalf("model registry count = %d, want 2", len(got))
	}
}

func modelRegistryTestConfig(kind string) *config.Config {
	return &config.Config{
		Routing: config.RoutingConfig{
			MaxAttempts:  3,
			DefaultRoute: "test-backend:test-model",
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind:    strings.TrimSpace(kind),
				ID:      "test-backend",
				Enabled: true,
			}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
}

func mustYAMLNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	return node
}

func writeModelRegistryCache(t *testing.T, path string, snap modelregistry.Snapshot) {
	t.Helper()
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readModelRegistryCache(t *testing.T, path string) modelregistry.Snapshot {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snap modelregistry.Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func closeModelRegistryBuilt(t *testing.T, b *runtimebundle.Built) func() {
	t.Helper()
	return func() {
		for i := len(b.Closers) - 1; i >= 0; i-- {
			if err := b.Closers[i](); err != nil {
				t.Fatalf("closer: %v", err)
			}
		}
	}
}

type runtimeBundleCountingInventory struct {
	mu     sync.Mutex
	calls  int
	err    error
	models []modelinventory.Model
}

func delayedBackendFactory(prefix, modelID string, delay time.Duration) pluginreg.BackendFactory {
	return func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{prefix},
			ModelInventory: delayedRuntimeBundleInventory{
				modelID: modelID,
				delay:   delay,
			},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}
}

type delayedRuntimeBundleInventory struct {
	modelID string
	delay   time.Duration
}

func (p delayedRuntimeBundleInventory) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	select {
	case <-ctx.Done():
		return modelinventory.Snapshot{}, ctx.Err()
	case <-time.After(p.delay):
		return modelinventory.Snapshot{
			Source:   modelinventory.SourceRemote,
			LoadedAt: time.Unix(500, 0).UTC(),
			Models: []modelinventory.Model{{
				CanonicalID: p.modelID,
				NativeID:    strings.TrimPrefix(p.modelID, "vendor/"),
			}},
		}, nil
	}
}

func (p *runtimeBundleCountingInventory) LoadModels(context.Context) (modelinventory.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.err != nil {
		return modelinventory.Snapshot{}, p.err
	}
	return modelinventory.Snapshot{
		Source:   modelinventory.SourceRemote,
		LoadedAt: time.Unix(500, 0).UTC(),
		Models:   append([]modelinventory.Model(nil), p.models...),
	}, nil
}

func (p *runtimeBundleCountingInventory) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}
