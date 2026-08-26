package runtimebundle

import (
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactiondetect"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactioncompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	featurecompaction "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type dummyHandlerComposer struct{}

func (dummyHandlerComposer) Compose(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
	return http.NotFoundHandler(), nil
}

func TestCompileGeneration_BinderFailuresFailClosed(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	require.NoError(t, standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}))

	reasoningNode := reasoningYAMLForTypedNil(t, "egress-ref")
	var compactionNode yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("extractor:\n  enabled: true\n  route: inherit\n"), &compactionNode))

	t.Run("compaction continuity missing prerequisite fails CompileGeneration before publication", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Routing:    config.RoutingConfig{MaxAttempts: 3},
			Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
			Server:     config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
			Diagnostics: config.DiagnosticsConfig{
				Enabled:    true,
				HealthPath: "/healthz",
			},
			Plugins: config.PluginsConfig{
				Features: []config.PluginConfig{
					{ID: featurecompaction.ID, Enabled: true, Config: compactionNode},
				},
			},
		}
		require.NoError(t, config.Validate(cfg))

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg:  cfg,
			Log:  slog.Default(),
			Opts: &BuildOptions{PluginRegistry: reg},
		})
		require.NoError(t, err)
		ps.BranchCoordinator = nil
		t.Cleanup(func() { _ = ps.Close() })

		gen, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process: ps,
			Compose: dummyHandlerComposer{}.Compose,
		})
		require.Error(t, err)
		assert.Nil(t, gen)
		assert.Contains(t, err.Error(), "generation prerequisite")
	})

	t.Run("reasoning compression missing capability fails CompileGeneration before publication", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Routing:    config.RoutingConfig{MaxAttempts: 3},
			Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
			Server:     config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
			Diagnostics: config.DiagnosticsConfig{
				Enabled:    true,
				HealthPath: "/healthz",
			},
			Plugins: config.PluginsConfig{
				Features: []config.PluginConfig{
					{ID: reasoningpreservation.ID, Enabled: true, Config: reasoningNode},
				},
			},
		}
		require.NoError(t, config.Validate(cfg))

		prod := ProductionOptions{
			ReasoningCompression: ReasoningCompressionOptions{
				EgressPolicies: map[string]reasoningpreservation.EgressPolicy{
					"egress-ref": charEgressPolicy{version: "v1"},
				},
				MatcherResolver: charMatcherResolver{},
			},
		}

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg:  cfg,
			Log:  slog.Default(),
			Opts: &BuildOptions{PluginRegistry: reg, Production: prod},
		})
		require.NoError(t, err)
		ps.BackgroundAux = nil
		t.Cleanup(func() { _ = ps.Close() })

		gen, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process: ps,
			Compose: dummyHandlerComposer{}.Compose,
		})
		require.Error(t, err)
		assert.Nil(t, gen)
		assert.Contains(t, err.Error(), "BackgroundAux")
	})

	t.Run("complete capabilities successfully binds both generation binders", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Routing:    config.RoutingConfig{MaxAttempts: 3},
			Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
			Server:     config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
			Diagnostics: config.DiagnosticsConfig{
				Enabled:    true,
				HealthPath: "/healthz",
			},
			Plugins: config.PluginsConfig{
				Features: []config.PluginConfig{
					{ID: featurecompaction.ID, Enabled: true, Config: compactionNode},
					{ID: reasoningpreservation.ID, Enabled: true, Config: reasoningNode},
				},
			},
		}
		require.NoError(t, config.Validate(cfg))

		scheduler, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner {
			return fixedRunnerForTypedNil{id: "full-gen"}
		}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 4, MaxResults: 10})
		require.NoError(t, err)
		t.Cleanup(func() { _ = scheduler.Close() })

		coord, err := compactioncontinuity.NewBranchCoordinator(context.Background(), compactioncontinuity.Config{})
		require.NoError(t, err)

		parentPort, err := compactioncompose.NewCompactionContinuityParentPort(coord)
		require.NoError(t, err)

		prod := ProductionOptions{
			ReasoningCompression: ReasoningCompressionOptions{
				EgressPolicies: map[string]reasoningpreservation.EgressPolicy{
					"egress-ref": charEgressPolicy{version: "v1"},
				},
				MatcherResolver: charMatcherResolver{},
			},
		}
		opts := &BuildOptions{PluginRegistry: reg, Production: prod}

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg:           cfg,
			Log:           slog.Default(),
			Opts:          opts,
			BackgroundAux: scheduler,
		})
		require.NoError(t, err)
		ps.CompactionDetector = compactiondetect.New(compactiondetect.Config{})
		ps.BranchCoordinator = coord
		ps.CompactionParentPort = parentPort
		t.Cleanup(func() { _ = ps.Close() })

		gen, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process: ps,
			Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
			},
		})
		require.NoError(t, err)
		require.NotNil(t, gen)
		t.Cleanup(func() { _ = gen.Close() })
	})
}
