package stdhttp

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	coreconfig "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
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

func TestReleaseBuiltResources_invokesBackendCloseOnceReverseOrder(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var closeOrder []string
	var closeCalls atomic.Int32

	reg := pluginreg.NewRegistry()
	for _, factoryID := range []string{"std-a", "std-b"} {
		id := factoryID
		if err := reg.RegisterBackend(id, func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
			return execbackend.Backend{
				Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				BackendPrefixes: []string{id},
				ModelInventory: modelinventory.StaticProvider{
					Source: modelinventory.SourceStaticBuiltin,
					Models: []modelinventory.Model{{
						CanonicalID: "test/model",
						NativeID:    "model",
						DisplayName: "Test Model",
					}},
				},
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				},
				Close: func() error {
					closeCalls.Add(1)
					mu.Lock()
					closeOrder = append(closeOrder, id)
					mu.Unlock()
					return nil
				},
			}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &coreconfig.Config{
		Server:     coreconfig.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    coreconfig.RoutingConfig{MaxAttempts: 3},
		Continuity: coreconfig.ContinuityConfig{InMemory: true, Store: "memory"},
		Plugins: coreconfig.PluginsConfig{Backends: []coreconfig.PluginConfig{
			{Kind: "std-a", ID: "a", Enabled: true, Config: empty},
			{Kind: "std-b", ID: "b", Enabled: true, Config: empty},
		}},
	}
	if err := coreconfig.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	log := testkit.DiscardLogger()
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), log, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}

	var once sync.Once
	releaseBuiltResources(log, built, &once)
	releaseBuiltResources(log, built, &once)

	if got := closeCalls.Load(); got != 2 {
		t.Fatalf("backend Close calls=%d want 2 (one each, once via shutdown)", got)
	}
	mu.Lock()
	got := append([]string(nil), closeOrder...)
	mu.Unlock()
	want := []string{"std-b", "std-a"}
	if !slices.Equal(got, want) {
		t.Fatalf("stdhttp shutdown close order=%v want %v", got, want)
	}
}
