package runtimebundle_test

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func TestBuild_LifecycleCleanupOnLaterAssemblyFailure(t *testing.T) {
	t.Parallel()
	factoryID := "lifecycle-cleanup-" + strings.ReplaceAll(t.Name(), "/", "-")
	reg := pluginreg.NewRegistry()
	var cleaned atomic.Int64
	err := reg.RegisterLifecycleBackend(factoryID, func(_ string, _ yaml.Node, _ *http.Client, _ pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
		return pluginreg.BackendBuildResult{
			Backend: execbackend.Backend{
				Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				BackendPrefixes: []string{factoryID},
				ModelInventory: modelinventory.StaticProvider{
					Source: modelinventory.SourceStaticBuiltin,
					Models: []modelinventory.Model{{
						CanonicalID: "test/model", NativeID: "model", DisplayName: "Test Model",
					}},
				},
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return nil, nil
				},
			},
			Cleanup: func() error {
				cleaned.Add(1)
				return nil
			},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Accounting: config.AccountingConfig{StrictAuthoritative: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: factoryID, ID: "be", Enabled: true,
		}}},
	}
	_, err = runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err == nil || !strings.Contains(err.Error(), "strict_authoritative requires billing finalizer") {
		t.Fatalf("Build err = %v, want strict_authoritative failure", err)
	}
	if cleaned.Load() != 1 {
		t.Fatalf("plugin BuildResult cleanup ran %d times, want 1", cleaned.Load())
	}
}
