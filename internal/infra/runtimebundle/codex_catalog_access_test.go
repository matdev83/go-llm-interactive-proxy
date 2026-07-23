package runtimebundle_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/codexcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func TestBuild_disabledBackendInstanceIsNotBuiltOrEnumerated(t *testing.T) {
	t.Parallel()

	var factoryCalls atomic.Int32
	var inventoryCalls atomic.Int32
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("local-agent-test", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		factoryCalls.Add(1)
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"local-agent-test"},
			ModelInventory: &countingAtomicInventory{
				calls: &inventoryCalls,
				models: []modelinventory.Model{{
					CanonicalID: "local/agent",
					NativeID:    "agent",
				}},
			},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterBackend("test-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-inventory"},
			ModelInventory: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "vendor/ok", NativeID: "ok"},
			}},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	cfg := modelRegistryTestConfig("test-inventory")
	cfg.Plugins.Backends = append(cfg.Plugins.Backends, config.PluginConfig{
		Kind:    "local-agent-test",
		ID:      "disabled-local-agent",
		Enabled: false,
	})

	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	t.Cleanup(closeModelRegistryBuilt(t, b))

	if factoryCalls.Load() != 0 {
		t.Fatalf("disabled local-agent factory calls = %d, want 0", factoryCalls.Load())
	}
	if inventoryCalls.Load() != 0 {
		t.Fatalf("disabled local-agent inventory calls = %d, want 0", inventoryCalls.Load())
	}
	if _, ok := b.ModelRegistry.Lookup("local/agent"); ok {
		t.Fatal("disabled local-agent model must not appear in registry")
	}
}

func TestBuild_enabledLocalAgentInventoryEnumeratedOnSingleUser(t *testing.T) {
	t.Parallel()

	var inventoryCalls atomic.Int32
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackendWithProfile("cursorcliacp", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"cursorcliacp"},
			ModelInventory: &countingAtomicInventory{
				calls: &inventoryCalls,
				models: []modelinventory.Model{{
					CanonicalID: "cursor/composer-2",
					NativeID:    "composer-2",
				}},
			},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}, pluginreg.BackendSecurityProfile{
		CredentialMode: pluginreg.CredentialNone,
		AccessScope:    pluginreg.BackendAccessLocalOnly,
	}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "single_user"},
		Server:     config.ServerConfig{Address: "localhost:8080"},
		Routing:    config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "cursorcliacp:composer-2"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: "cursorcliacp", ID: "cursor-cli", Enabled: true,
		}}},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	t.Cleanup(closeModelRegistryBuilt(t, b))

	if inventoryCalls.Load() != 1 {
		t.Fatalf("single_user local-agent inventory calls = %d, want 1", inventoryCalls.Load())
	}
	if _, ok := b.ModelRegistry.Lookup("cursor/composer-2"); !ok {
		t.Fatal("Lookup(cursor/composer-2) ok = false")
	}
}

func TestBuild_enabledLocalAgentInventoryEnumeratedOnDefaultAccessMode(t *testing.T) {
	t.Parallel()

	var inventoryCalls atomic.Int32
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackendWithProfile("agycliacp", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"agycliacp"},
			ModelInventory: &countingAtomicInventory{
				calls: &inventoryCalls,
				models: []modelinventory.Model{{
					CanonicalID: "agy/gemini-3-flash",
					NativeID:    "gemini-3-flash",
				}},
			},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}, pluginreg.BackendSecurityProfile{
		CredentialMode: pluginreg.CredentialNone,
		AccessScope:    pluginreg.BackendAccessLocalOnly,
	}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		// Empty access.mode defaults to single_user.
		Server:     config.ServerConfig{Address: "localhost:8080"},
		Routing:    config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "agycliacp:gemini-3-flash"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: "agycliacp", ID: "agy-cli", Enabled: true,
		}}},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	t.Cleanup(closeModelRegistryBuilt(t, b))

	if inventoryCalls.Load() != 1 {
		t.Fatalf("default access.mode inventory calls = %d, want 1", inventoryCalls.Load())
	}
}

func TestBuild_localOnlyBackend_multiUserRejectsBeforeFactoryOrInventory(t *testing.T) {
	t.Parallel()

	var factoryCalls atomic.Int32
	var inventoryCalls atomic.Int32
	var catalogLoads atomic.Int32
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackendWithProfile("openai-codex-app-server", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		factoryCalls.Add(1)
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"openai-codex-app-server"},
			ModelInventory: &countingAtomicInventory{
				calls: &inventoryCalls,
				models: []modelinventory.Model{{
					CanonicalID: "openai/gpt-5.5",
					NativeID:    "gpt-5.5",
				}},
			},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}, pluginreg.BackendSecurityProfile{
		CredentialMode: pluginreg.CredentialNone,
		AccessScope:    pluginreg.BackendAccessLocalOnly,
	}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "multi_user"},
		Server:     config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: "openai-codex-app-server", ID: "codex-app", Enabled: true,
		}}},
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Auth:           runtimebundle.AuthOptions{RemoteDecider: &testkit.StubRemoteDecider{}},
		Testing: runtimebundle.TestingOptions{
			CodexCatalogLoad: countingCodexCatalogLoad(&catalogLoads),
		},
	})
	if err == nil || !errors.Is(err, runtimebundle.ErrLocalOnlyBackendDisallowedMultiUser) {
		t.Fatalf("want %v, got %v", runtimebundle.ErrLocalOnlyBackendDisallowedMultiUser, err)
	}
	if factoryCalls.Load() != 0 {
		t.Fatalf("multi_user local-only factory calls = %d, want 0", factoryCalls.Load())
	}
	if inventoryCalls.Load() != 0 {
		t.Fatalf("multi_user local-only inventory calls = %d, want 0", inventoryCalls.Load())
	}
	if catalogLoads.Load() != 0 {
		t.Fatalf("multi_user codex catalog loads = %d, want 0 (security rejects before model runtime)", catalogLoads.Load())
	}
}

func TestBuild_codexCatalogDiscovery_skippedOnMultiUserWithoutLocalConsumers(t *testing.T) {
	t.Parallel()

	var catalogLoads atomic.Int32
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackendWithProfile("test-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-inventory"},
			ModelInventory: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "vendor/ok", NativeID: "ok"},
			}},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic}); err != nil {
		t.Fatal(err)
	}

	cfg := modelRegistryTestConfig("test-inventory")
	cfg.Access = config.AccessConfig{Mode: "multi_user"}
	cfg.Server = config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal}
	cfg.Auth = config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"}

	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Auth:           runtimebundle.AuthOptions{RemoteDecider: &testkit.StubRemoteDecider{}},
		Testing: runtimebundle.TestingOptions{
			CodexCatalogLoad: countingCodexCatalogLoad(&catalogLoads),
		},
	})
	t.Cleanup(closeModelRegistryBuilt(t, b))

	if catalogLoads.Load() != 0 {
		t.Fatalf("multi_user codex catalog loads = %d, want 0", catalogLoads.Load())
	}
}

func TestBuild_codexCatalogDiscovery_skippedForEnabledUnregisteredConsumer(t *testing.T) {
	t.Parallel()

	var catalogLoads atomic.Int32
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackendWithProfile("test-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-inventory"},
			ModelInventory: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "vendor/ok", NativeID: "ok"},
			}},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic}); err != nil {
		t.Fatal(err)
	}

	cfg := modelRegistryTestConfig("test-inventory")
	cfg.Access = config.AccessConfig{Mode: "single_user"}
	cfg.Plugins.Backends = append(cfg.Plugins.Backends, config.PluginConfig{
		Kind: "openai-codex", ID: "codex-unregistered", Enabled: true,
	})

	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Testing: runtimebundle.TestingOptions{
			CodexCatalogLoad: countingCodexCatalogLoad(&catalogLoads),
		},
	})
	if err == nil {
		t.Fatal("Build() error = nil, want failure for unregistered openai-codex")
	}
	if catalogLoads.Load() != 0 {
		t.Fatalf("codex catalog loads for enabled unregistered consumer = %d, want 0", catalogLoads.Load())
	}
}

func TestBuild_codexCatalogDiscovery_skippedWithoutEnabledConsumer(t *testing.T) {
	t.Parallel()

	var catalogLoads atomic.Int32
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-inventory"},
			ModelInventory: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "vendor/ok", NativeID: "ok"},
			}},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := reg.RegisterBackendWithProfile("openai-codex", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		t.Fatal("disabled openai-codex factory must not be built")
		return execbackend.Backend{}, errors.New("not used")
	}, pluginreg.BackendSecurityProfile{
		CredentialMode: pluginreg.CredentialStatic,
		AccessScope:    pluginreg.BackendAccessLocalOnly,
	}); err != nil {
		t.Fatal(err)
	}

	cfg := modelRegistryTestConfig("test-inventory")
	cfg.Access = config.AccessConfig{Mode: "single_user"}
	cfg.Plugins.Backends = append(cfg.Plugins.Backends, config.PluginConfig{
		Kind: "openai-codex", ID: "codex-disabled", Enabled: false,
	})

	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Testing: runtimebundle.TestingOptions{
			CodexCatalogLoad: countingCodexCatalogLoad(&catalogLoads),
		},
	})
	t.Cleanup(closeModelRegistryBuilt(t, b))

	if catalogLoads.Load() != 0 {
		t.Fatalf("codex catalog loads without enabled consumer = %d, want 0", catalogLoads.Load())
	}
}

func TestBuild_codexCatalogDiscovery_runsOnSingleUserWithEnabledConsumer(t *testing.T) {
	t.Parallel()

	var catalogLoads atomic.Int32
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackendWithProfile("openai-codex", func(_ yaml.Node, _ *http.Client, deps pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		if deps.CodexModelCatalog == nil {
			t.Fatal("expected non-nil CodexModelCatalog for enabled openai-codex consumer")
		}
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"openai-codex"},
			ModelInventory: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "openai/gpt-5.5", NativeID: "gpt-5.5"},
			}},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}, pluginreg.BackendSecurityProfile{
		CredentialMode: pluginreg.CredentialStatic,
		AccessScope:    pluginreg.BackendAccessLocalOnly,
	}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "single_user"},
		Server:     config.ServerConfig{Address: "localhost:8080"},
		Routing:    config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "openai-codex:gpt-5.5"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: "openai-codex", ID: "codex", Enabled: true,
		}}},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Testing: runtimebundle.TestingOptions{
			CodexCatalogLoad: countingCodexCatalogLoad(&catalogLoads),
		},
	})
	t.Cleanup(closeModelRegistryBuilt(t, b))

	if catalogLoads.Load() != 1 {
		t.Fatalf("single_user codex catalog loads = %d, want 1", catalogLoads.Load())
	}
}

func TestBuild_codexCatalogDiscovery_runsOnDefaultAccessModeWithEnabledAppServerConsumer(t *testing.T) {
	t.Parallel()

	var catalogLoads atomic.Int32
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackendWithProfile("openai-codex-app-server", func(_ yaml.Node, _ *http.Client, deps pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		if deps.CodexModelCatalog == nil {
			t.Fatal("expected non-nil CodexModelCatalog for enabled app-server consumer")
		}
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{"openai-codex-app-server"},
			ModelInventory: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "openai/gpt-5.5", NativeID: "gpt-5.5"},
			}},
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}, pluginreg.BackendSecurityProfile{
		CredentialMode: pluginreg.CredentialNone,
		AccessScope:    pluginreg.BackendAccessLocalOnly,
	}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "localhost:8080"},
		Routing:    config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "openai-codex-app-server:gpt-5.5"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: "openai-codex-app-server", ID: "codex-app", Enabled: true,
		}}},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Testing: runtimebundle.TestingOptions{
			CodexCatalogLoad: countingCodexCatalogLoad(&catalogLoads),
		},
	})
	t.Cleanup(closeModelRegistryBuilt(t, b))

	if catalogLoads.Load() != 1 {
		t.Fatalf("default access.mode codex catalog loads = %d, want 1", catalogLoads.Load())
	}
}

type countingAtomicInventory struct {
	calls  *atomic.Int32
	models []modelinventory.Model
}

func (p *countingAtomicInventory) LoadModels(context.Context) (modelinventory.Snapshot, error) {
	p.calls.Add(1)
	return modelinventory.Snapshot{
		Source: modelinventory.SourceRemote,
		Models: append([]modelinventory.Model(nil), p.models...),
	}, nil
}

func countingCodexCatalogLoad(calls *atomic.Int32) runtimebundle.CodexCatalogLoadFunc {
	return func(ctx context.Context, opts codexcatalog.LoadOptions) (*codexcatalog.Catalog, codexcatalog.Source, error) {
		calls.Add(1)
		// Never invoke real `codex debug models`; only prove the composition-root call gate.
		return codexcatalog.Load(ctx, codexcatalog.LoadOptions{Enabled: false, FallbackPath: opts.FallbackPath})
	}
}
