package runtimebundle

import (
	"context"
	"log/slog"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactioncompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Characterization stubs for reasoning binder ---

type charObserverFactory struct{ id string }

func (f charObserverFactory) ID() string                      { return f.id }
func (charObserverFactory) Order() int                        { return 0 }
func (charObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

type charAttemptTransform struct{ id string }

func (t charAttemptTransform) ID() string                      { return t.id }
func (charAttemptTransform) Order() int                        { return 0 }
func (charAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charAttemptTransform) HandleAttempt(context.Context, *lipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{}, nil
}

type charTerminalProvider struct{ id string }

func (p charTerminalProvider) ID() string { return p.id }
func (charTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop}, nil
}

type charEgressPolicy struct{ version string }

func (p charEgressPolicy) Decide(context.Context, reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: p.version}, nil
}

type charMatcherResolver struct{}

func (charMatcherResolver) Resolve(context.Context) (sdk.Matcher, error) {
	return charMatcher{}, nil
}

type charMatcher struct{}

func (charMatcher) ScanBytes(context.Context, []byte) ([]sdk.Finding, error)  { return nil, nil }
func (charMatcher) ScanString(context.Context, string) ([]sdk.Finding, error) { return nil, nil }
func (charMatcher) RedactBytes(_ context.Context, b []byte) ([]byte, []sdk.Finding, error) {
	return b, nil, nil
}

func (charMatcher) RedactString(_ context.Context, s string) (string, []sdk.Finding, error) {
	return s, nil, nil
}

func setupReasoningTestServices(t *testing.T, egressRef string) (*ProcessServices, lipsdk.Registration, auxiliary.BackgroundClient, auxiliary.BackgroundPoller) {
	t.Helper()
	reg := pluginreg.NewRegistry()
	require.NoError(t, standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}))

	node := reasoningYAMLForTypedNil(t, egressRef)
	pluginReg := lipsdk.Registration{
		ID:          reasoningpreservation.ID,
		FactoryKind: reasoningpreservation.ID,
		Kind:        lipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      lipsdk.ConfigPayload{Node: node},
	}

	scheduler, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner {
		return fixedRunnerForTypedNil{id: "char-runner"}
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 4, MaxResults: 10})
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheduler.Close() })

	prod := ProductionOptions{
		ReasoningCompression: ReasoningCompressionOptions{
			EgressPolicies: map[string]reasoningpreservation.EgressPolicy{
				egressRef: charEgressPolicy{version: "v1"},
			},
			MatcherResolver: charMatcherResolver{},
		},
	}
	opts := &BuildOptions{PluginRegistry: reg, Production: prod}

	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:     config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
		Diagnostics: config.DiagnosticsConfig{
			Enabled:    true,
			HealthPath: "/healthz",
		},
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: node}},
		},
	}
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg:           cfg,
		Log:           slog.Default(),
		Opts:          opts,
		BackgroundAux: scheduler,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	genRunner := compactioncompose.NewGenerationExecutorRunner()
	bound := scheduler.BindRunner(genRunner)
	bPoller, ok := bound.(auxiliary.BackgroundPoller)
	require.True(t, ok, "bound client must implement BackgroundPoller")

	return ps, pluginReg, bound, bPoller
}

func TestReasoningCompression_ReplaceByIdentity(t *testing.T) {
	t.Parallel()
	ps, reg, client, poller := setupReasoningTestServices(t, "ref-replace")

	reasoningObsID := reasoningpreservation.ID + "-observer"
	reasoningXformID := reasoningpreservation.ID + "-transform"

	tests := []struct {
		name          string
		initialObs    []response.StreamObserverFactory
		initialXforms []request.AttemptTransform
		wantObsIDs    []string
		wantXformIDs  []string
	}{
		{
			name: "replaces existing reasoning participants in middle preserving surrounding third-party participants",
			initialObs: []response.StreamObserverFactory{
				charObserverFactory{id: "custom-obs-1"},
				charObserverFactory{id: reasoningObsID},
				charObserverFactory{id: "custom-obs-2"},
			},
			initialXforms: []request.AttemptTransform{
				charAttemptTransform{id: "custom-xform-1"},
				charAttemptTransform{id: reasoningXformID},
				charAttemptTransform{id: "custom-xform-2"},
			},
			wantObsIDs:   []string{"custom-obs-1", "custom-obs-2", reasoningObsID},
			wantXformIDs: []string{"custom-xform-1", "custom-xform-2", reasoningXformID},
		},
		{
			name: "no prior reasoning participants appends new participants to end",
			initialObs: []response.StreamObserverFactory{
				charObserverFactory{id: "custom-obs-1"},
				charObserverFactory{id: "custom-obs-2"},
			},
			initialXforms: []request.AttemptTransform{
				charAttemptTransform{id: "custom-xform-1"},
				charAttemptTransform{id: "custom-xform-2"},
			},
			wantObsIDs:   []string{"custom-obs-1", "custom-obs-2", reasoningObsID},
			wantXformIDs: []string{"custom-xform-1", "custom-xform-2", reasoningXformID},
		},
		{
			name: "only reasoning participants replaced with single bound participant",
			initialObs: []response.StreamObserverFactory{
				charObserverFactory{id: reasoningObsID},
			},
			initialXforms: []request.AttemptTransform{
				charAttemptTransform{id: reasoningXformID},
			},
			wantObsIDs:   []string{reasoningObsID},
			wantXformIDs: []string{reasoningXformID},
		},
		{
			name: "multiple duplicate reasoning participants all stripped and replaced by single bound participant at end",
			initialObs: []response.StreamObserverFactory{
				charObserverFactory{id: reasoningObsID},
				charObserverFactory{id: "custom-obs-1"},
				charObserverFactory{id: reasoningObsID},
				charObserverFactory{id: "custom-obs-2"},
				charObserverFactory{id: reasoningObsID},
			},
			initialXforms: []request.AttemptTransform{
				charAttemptTransform{id: reasoningXformID},
				charAttemptTransform{id: "custom-xform-1"},
				charAttemptTransform{id: reasoningXformID},
				charAttemptTransform{id: "custom-xform-2"},
				charAttemptTransform{id: reasoningXformID},
			},
			wantObsIDs:   []string{"custom-obs-1", "custom-obs-2", reasoningObsID},
			wantXformIDs: []string{"custom-xform-1", "custom-xform-2", reasoningXformID},
		},
		{
			name:          "empty surface receives single bound participant",
			initialObs:    nil,
			initialXforms: nil,
			wantObsIDs:    []string{reasoningObsID},
			wantXformIDs:  []string{reasoningXformID},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			merged := featurebundle.MergedFeatureSurface{
				AttemptTransforms: tc.initialXforms,
			}
			cs := lipfeature.NewContributionSet()
			if len(tc.initialObs) > 0 {
				require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneStreamObserverFactories, "char-init", tc.initialObs))
			}
			if len(tc.initialXforms) > 0 {
				require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneAttemptTransforms, "char-init", tc.initialXforms))
			}
			genMerged := featurebundle.GeneratedMergeSurface{
				Frozen: cs.Freeze(),
			}
			resMerged, resGen, err := bindReasoningPreservationCompression(merged, genMerged, ps, []lipsdk.Registration{reg}, client, poller)
			require.NoError(t, err)

			gotObs := lipfeature.Get(resGen.Frozen, lipfeature.PlaneStreamObserverFactories)
			gotObsIDs := make([]string, len(gotObs))
			for i, f := range gotObs {
				require.NotNil(t, f)
				gotObsIDs[i] = f.ID()
			}
			assert.Equal(t, tc.wantObsIDs, gotObsIDs)

			gotXforms := lipfeature.Get(resGen.Frozen, lipfeature.PlaneAttemptTransforms)
			gotXformGenIDs := make([]string, len(gotXforms))
			for i, x := range gotXforms {
				require.NotNil(t, x)
				gotXformGenIDs[i] = x.ID()
			}
			assert.Equal(t, tc.wantXformIDs, gotXformGenIDs)

			gotXformIDs := make([]string, len(resMerged.AttemptTransforms))
			for i, x := range resMerged.AttemptTransforms {
				require.NotNil(t, x)
				gotXformIDs[i] = x.ID()
			}
			assert.Equal(t, tc.wantXformIDs, gotXformIDs)
		})
	}
}

func TestReasoningCompression_Idempotence(t *testing.T) {
	t.Parallel()
	ps, reg, client, poller := setupReasoningTestServices(t, "ref-idempotence")

	reasoningObsID := reasoningpreservation.ID + "-observer"
	reasoningXformID := reasoningpreservation.ID + "-transform"

	initial := featurebundle.MergedFeatureSurface{
		AttemptTransforms: []request.AttemptTransform{
			charAttemptTransform{id: "custom-xform-1"},
			charAttemptTransform{id: reasoningXformID},
			charAttemptTransform{id: "custom-xform-2"},
		},
	}
	cs := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneAttemptTransforms, "char-init", []request.AttemptTransform{
		charAttemptTransform{id: "custom-xform-1"},
		charAttemptTransform{id: reasoningXformID},
		charAttemptTransform{id: "custom-xform-2"},
	}))
	require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneStreamObserverFactories, "char-init", []response.StreamObserverFactory{
		charObserverFactory{id: "custom-obs-1"},
		charObserverFactory{id: reasoningObsID},
		charObserverFactory{id: "custom-obs-2"},
	}))
	initialGen := featurebundle.GeneratedMergeSurface{
		Frozen: cs.Freeze(),
	}

	res1, resGen1, err := bindReasoningPreservationCompression(initial, initialGen, ps, []lipsdk.Registration{reg}, client, poller)
	require.NoError(t, err)

	res2, resGen2, err := bindReasoningPreservationCompression(res1, resGen1, ps, []lipsdk.Registration{reg}, client, poller)
	require.NoError(t, err)

	res3, resGen3, err := bindReasoningPreservationCompression(res2, resGen2, ps, []lipsdk.Registration{reg}, client, poller)
	require.NoError(t, err)

	require.Len(t, res1.AttemptTransforms, 3)
	require.Len(t, res2.AttemptTransforms, 3)
	require.Len(t, res3.AttemptTransforms, 3)

	genXforms1 := lipfeature.Get(resGen1.Frozen, lipfeature.PlaneAttemptTransforms)
	genXforms2 := lipfeature.Get(resGen2.Frozen, lipfeature.PlaneAttemptTransforms)
	genXforms3 := lipfeature.Get(resGen3.Frozen, lipfeature.PlaneAttemptTransforms)
	require.Len(t, genXforms1, 3)
	require.Len(t, genXforms2, 3)
	require.Len(t, genXforms3, 3)

	obs1 := lipfeature.Get(resGen1.Frozen, lipfeature.PlaneStreamObserverFactories)
	obs2 := lipfeature.Get(resGen2.Frozen, lipfeature.PlaneStreamObserverFactories)
	obs3 := lipfeature.Get(resGen3.Frozen, lipfeature.PlaneStreamObserverFactories)
	require.Len(t, obs1, 3)
	require.Len(t, obs2, 3)
	require.Len(t, obs3, 3)

	for i := range 3 {
		assert.Equal(t, res1.AttemptTransforms[i].ID(), res2.AttemptTransforms[i].ID())
		assert.Equal(t, res1.AttemptTransforms[i].ID(), res3.AttemptTransforms[i].ID())
		assert.Equal(t, genXforms1[i].ID(), genXforms2[i].ID())
		assert.Equal(t, genXforms1[i].ID(), genXforms3[i].ID())
		assert.Equal(t, obs1[i].ID(), obs2[i].ID())
		assert.Equal(t, obs1[i].ID(), obs3[i].ID())
	}
}

func TestReasoningCompression_FailBeforeMutate_CandidateUnmodified(t *testing.T) {
	t.Parallel()
	ps, reg, client, poller := setupReasoningTestServices(t, "ref-fail-mutate")

	initialSurface := featurebundle.MergedFeatureSurface{
		TerminalDecisionProvider: charTerminalProvider{id: "initial-provider"},
		AttemptTransforms: []request.AttemptTransform{
			charAttemptTransform{id: "custom-xform"},
		},
	}
	cs := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneStreamObserverFactories, "char-init", []response.StreamObserverFactory{
		charObserverFactory{id: "custom-obs"},
	}))
	initialGen := featurebundle.GeneratedMergeSurface{
		Frozen: cs.Freeze(),
	}
	snapshot := initialSurface
	snapshotGen := initialGen

	t.Run("BindAttemptTransforms rejects nil elements leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		candGen := initialGen
		resG, err := candGen.BindAttemptTransforms("invalid-provider", []request.AttemptTransform{nil})
		require.Error(t, err)
		assert.Equal(t, featurebundle.GeneratedMergeSurface{}, resG, "failed composition must return empty surface")
		assert.Equal(t, snapshotGen.Frozen, candGen.Frozen, "original candidate must remain unmodified")
	})

	t.Run("invalid config yaml returns error and empty surface leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		cand := initialSurface
		candGen := initialGen
		var badNode yaml.Node
		_ = yaml.Unmarshal([]byte("action: [invalid-unbalanced\n"), &badNode)

		badReg := lipsdk.Registration{
			ID:          reasoningpreservation.ID,
			FactoryKind: reasoningpreservation.ID,
			Kind:        lipsdk.PluginKindFeature,
			Enabled:     true,
			Config:      lipsdk.ConfigPayload{Node: badNode},
		}

		res, resG, err := bindReasoningPreservationCompression(cand, candGen, ps, []lipsdk.Registration{badReg}, client, poller)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reasoningpreservation: config")
		assert.Equal(t, featurebundle.MergedFeatureSurface{}, res)
		assert.Equal(t, featurebundle.GeneratedMergeSurface{}, resG)
		assert.Equal(t, snapshot, cand, "original candidate must remain unmodified")
		assert.Equal(t, snapshotGen.Frozen, candGen.Frozen, "original generated candidate must remain unmodified")
	})

	t.Run("nil background aux client/poller returns error and empty surface leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		cand := initialSurface
		candGen := initialGen
		res, resG, err := bindReasoningPreservationCompression(cand, candGen, ps, []lipsdk.Registration{reg}, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BackgroundAux")
		assert.Equal(t, featurebundle.MergedFeatureSurface{}, res)
		assert.Equal(t, featurebundle.GeneratedMergeSurface{}, resG)
		assert.Equal(t, snapshot, cand, "original candidate must remain unmodified")
		assert.Equal(t, snapshotGen.Frozen, candGen.Frozen, "original generated candidate must remain unmodified")
	})

	t.Run("missing egress policy returns error and empty surface leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		cand := initialSurface
		candGen := initialGen
		nodeMissingEgress := reasoningYAMLForTypedNil(t, "nonexistent-egress-ref")
		missingReg := lipsdk.Registration{
			ID:          reasoningpreservation.ID,
			FactoryKind: reasoningpreservation.ID,
			Kind:        lipsdk.PluginKindFeature,
			Enabled:     true,
			Config:      lipsdk.ConfigPayload{Node: nodeMissingEgress},
		}

		res, resG, err := bindReasoningPreservationCompression(cand, candGen, ps, []lipsdk.Registration{missingReg}, client, poller)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "EgressPolicy")
		assert.Equal(t, featurebundle.MergedFeatureSurface{}, res)
		assert.Equal(t, featurebundle.GeneratedMergeSurface{}, resG)
		assert.Equal(t, snapshot, cand, "original candidate must remain unmodified")
		assert.Equal(t, snapshotGen.Frozen, candGen.Frozen, "original generated candidate must remain unmodified")
	})

	t.Run("missing matcher resolver returns error and empty surface leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		cand := initialSurface
		candGen := initialGen
		psNoResolver := &ProcessServices{
			opts: &BuildOptions{
				Production: ProductionOptions{
					ReasoningCompression: ReasoningCompressionOptions{
						EgressPolicies:  map[string]reasoningpreservation.EgressPolicy{"ref-fail-mutate": charEgressPolicy{}},
						MatcherResolver: nil,
					},
				},
			},
		}
		res, resG, err := bindReasoningPreservationCompression(cand, candGen, psNoResolver, []lipsdk.Registration{reg}, client, poller)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SecretGuard MatcherResolver")
		assert.Equal(t, featurebundle.MergedFeatureSurface{}, res)
		assert.Equal(t, featurebundle.GeneratedMergeSurface{}, resG)
		assert.Equal(t, snapshot, cand, "original candidate must remain unmodified")
		assert.Equal(t, snapshotGen.Frozen, candGen.Frozen, "original generated candidate must remain unmodified")
	})
}

func TestReasoningCompression_NilElementHandling(t *testing.T) {
	t.Parallel()
	ps, reg, client, poller := setupReasoningTestServices(t, "ref-nil-elem")

	reasoningObsID := reasoningpreservation.ID + "-observer"
	reasoningXformID := reasoningpreservation.ID + "-transform"

	t.Run("BindAttemptTransforms rejects nil elements under NilReject policy", func(t *testing.T) {
		t.Parallel()
		genMerged := featurebundle.GeneratedMergeSurface{}
		_, err := genMerged.BindAttemptTransforms(reasoningpreservation.ID, []request.AttemptTransform{nil})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be nil")
	})

	t.Run("bindReasoningPreservationCompression replaces reasoning participants in attempt transforms and observer factories safely", func(t *testing.T) {
		t.Parallel()
		m := featurebundle.MergedFeatureSurface{
			AttemptTransforms: []request.AttemptTransform{
				charAttemptTransform{id: "custom-xform"},
				charAttemptTransform{id: reasoningXformID},
			},
		}
		cs := lipfeature.NewContributionSet()
		require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneAttemptTransforms, "char-init", []request.AttemptTransform{
			charAttemptTransform{id: "custom-xform"},
			charAttemptTransform{id: reasoningXformID},
		}))
		require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneStreamObserverFactories, "char-init", []response.StreamObserverFactory{
			charObserverFactory{id: "custom-obs"},
			charObserverFactory{id: reasoningObsID},
		}))
		genMerged := featurebundle.GeneratedMergeSurface{
			Frozen: cs.Freeze(),
		}

		res, resGen, err := bindReasoningPreservationCompression(m, genMerged, ps, []lipsdk.Registration{reg}, client, poller)
		require.NoError(t, err)

		require.Len(t, res.AttemptTransforms, 2)
		assert.Equal(t, "custom-xform", res.AttemptTransforms[0].ID())
		assert.Equal(t, reasoningXformID, res.AttemptTransforms[1].ID())

		obs := lipfeature.Get(resGen.Frozen, lipfeature.PlaneStreamObserverFactories)
		require.Len(t, obs, 2)
		assert.Equal(t, "custom-obs", obs[0].ID())
		assert.Equal(t, reasoningObsID, obs[1].ID())
	})

	t.Run("BindStreamObserverFactories rejects nil elements under NilReject policy", func(t *testing.T) {
		t.Parallel()
		genMerged := featurebundle.GeneratedMergeSurface{}
		_, err := genMerged.BindStreamObserverFactories(reasoningpreservation.ID, []response.StreamObserverFactory{nil})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be nil")
	})
}

func TestReasoningCompression_DisabledNoOp(t *testing.T) {
	t.Parallel()
	ps, reg, client, poller := setupReasoningTestServices(t, "ref-disabled")

	initial := featurebundle.MergedFeatureSurface{
		AttemptTransforms: []request.AttemptTransform{
			charAttemptTransform{id: "custom-xform"},
		},
	}
	cs := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneAttemptTransforms, "char-init", []request.AttemptTransform{
		charAttemptTransform{id: "custom-xform"},
	}))
	require.NoError(t, lipfeature.Contribute(cs, lipfeature.PlaneStreamObserverFactories, "char-init", []response.StreamObserverFactory{
		charObserverFactory{id: "custom-obs"},
	}))
	initialGen := featurebundle.GeneratedMergeSurface{
		Frozen: cs.Freeze(),
	}

	t.Run("disabled registration is a no-op", func(t *testing.T) {
		t.Parallel()
		disabledReg := reg
		disabledReg.Enabled = false

		res, resG, err := bindReasoningPreservationCompression(initial, initialGen, ps, []lipsdk.Registration{disabledReg}, client, poller)
		require.NoError(t, err)
		assert.True(t, reflect.DeepEqual(initial, res))
		assert.Equal(t, lipfeature.Get(initialGen.Frozen, lipfeature.PlaneStreamObserverFactories), lipfeature.Get(resG.Frozen, lipfeature.PlaneStreamObserverFactories))
	})

	t.Run("disabled compression in config is a no-op", func(t *testing.T) {
		t.Parallel()
		var node yaml.Node
		raw := `
action: restore
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 24h
  max_turns_per_session: 10
  max_reasoning_bytes_per_turn: 100000
  max_session_bytes: 1000000
compression:
  enabled: false
`
		require.NoError(t, yaml.Unmarshal([]byte(raw), &node))

		disabledCfgReg := lipsdk.Registration{
			ID:          reasoningpreservation.ID,
			FactoryKind: reasoningpreservation.ID,
			Kind:        lipsdk.PluginKindFeature,
			Enabled:     true,
			Config:      lipsdk.ConfigPayload{Node: node},
		}

		res, resG, err := bindReasoningPreservationCompression(initial, initialGen, ps, []lipsdk.Registration{disabledCfgReg}, client, poller)
		require.NoError(t, err)
		assert.True(t, reflect.DeepEqual(initial, res))
		assert.Equal(t, lipfeature.Get(initialGen.Frozen, lipfeature.PlaneStreamObserverFactories), lipfeature.Get(resG.Frozen, lipfeature.PlaneStreamObserverFactories))
	})
}

func TestReasoningCompression_LazyOpen(t *testing.T) {
	t.Parallel()
	ps, reg, client, poller := setupReasoningTestServices(t, "ref-lazy-open")

	merged := featurebundle.MergedFeatureSurface{}
	genMerged := featurebundle.GeneratedMergeSurface{}

	resMerged, resGen, err := bindReasoningPreservationCompression(merged, genMerged, ps, []lipsdk.Registration{reg}, client, poller)
	require.NoError(t, err)
	require.Len(t, resMerged.AttemptTransforms, 1)

	factories := lipfeature.Get(resGen.Frozen, lipfeature.PlaneStreamObserverFactories)
	require.Len(t, factories, 1)
	obsFactory := factories[0]
	require.NotNil(t, obsFactory)
	assert.Equal(t, reasoningpreservation.ID+"-observer", obsFactory.ID())

	// Prove lazy Open succeeds and returns a non-nil StreamObserver
	obs, err := obsFactory.Open(context.Background(), response.StreamMeta{
		TraceID: "t-1",
		ALegID:  "a-1",
		BLegID:  "b-1",
		Model:   "test-model",
	}, response.Services{})
	require.NoError(t, err)
	require.NotNil(t, obs, "bound reasoning stream observer factory must open non-nil StreamObserver")
}
