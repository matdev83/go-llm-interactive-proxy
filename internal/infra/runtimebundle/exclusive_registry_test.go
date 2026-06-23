package runtimebundle_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

func TestBuild_backendConstructionUsesInjectedRegistryNotDefault(t *testing.T) {
	t.Parallel()

	factoryID := "custom-registry-backend-" + strings.ReplaceAll(t.Name(), "/", "-")
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend(factoryID, func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
		_ = n
		_ = upstream
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{factoryID},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("exclusive-registry-probe")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	empty := pluginreg.NewRegistry()
	if _, err := empty.BuildBackend(factoryID, yaml.Node{}, nil); err == nil {
		t.Fatal("expected empty registry to omit custom factory id")
	}

	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{Kind: factoryID, ID: "only-instance", Enabled: true, Config: cfgNode},
			},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
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
	be, ok := b.Executor.Backends["only-instance"]
	if !ok {
		t.Fatal("expected backend instance")
	}
	call := lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}},
		Route:    lipapi.RouteIntent{Selector: "only-instance:model"},
	}
	_, err = be.Open(context.Background(), call, routing.AttemptCandidate{})
	if err == nil || !strings.Contains(err.Error(), "exclusive-registry-probe") {
		t.Fatalf("Open: %v", err)
	}
}
