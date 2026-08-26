package runtimebundle

import (
	"context"
	"errors"
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

func (o charObserverFactory) ID() string                      { return o.id }
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
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			merged := featurebundle.MergedFeatureSurface{
				StreamObserverFactories: tc.initialObs,
				AttemptTransforms:       tc.initialXforms,
			}
			res, err := bindReasoningPreservationCompression(merged, ps, []lipsdk.Registration{reg}, client, poller)
			require.NoError(t, err)

			gotObsIDs := make([]string, len(res.StreamObserverFactories))
			for i, f := range res.StreamObserverFactories {
				require.NotNil(t, f)
				gotObsIDs[i] = f.ID()
			}
			assert.Equal(t, tc.wantObsIDs, gotObsIDs)

			gotXformIDs := make([]string, len(res.AttemptTransforms))
			for i, x := range res.AttemptTransforms {
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
		StreamObserverFactories: []response.StreamObserverFactory{
			charObserverFactory{id: "custom-obs-1"},
			charObserverFactory{id: reasoningObsID},
			charObserverFactory{id: "custom-obs-2"},
		},
		AttemptTransforms: []request.AttemptTransform{
			charAttemptTransform{id: "custom-xform-1"},
			charAttemptTransform{id: reasoningXformID},
			charAttemptTransform{id: "custom-xform-2"},
		},
	}

	res1, err := bindReasoningPreservationCompression(initial, ps, []lipsdk.Registration{reg}, client, poller)
	require.NoError(t, err)

	res2, err := bindReasoningPreservationCompression(res1, ps, []lipsdk.Registration{reg}, client, poller)
	require.NoError(t, err)

	res3, err := bindReasoningPreservationCompression(res2, ps, []lipsdk.Registration{reg}, client, poller)
	require.NoError(t, err)

	require.Len(t, res1.StreamObserverFactories, 3)
	require.Len(t, res2.StreamObserverFactories, 3)
	require.Len(t, res3.StreamObserverFactories, 3)

	require.Len(t, res1.AttemptTransforms, 3)
	require.Len(t, res2.AttemptTransforms, 3)
	require.Len(t, res3.AttemptTransforms, 3)

	for i := 0; i < 3; i++ {
		assert.Equal(t, res1.StreamObserverFactories[i].ID(), res2.StreamObserverFactories[i].ID())
		assert.Equal(t, res1.StreamObserverFactories[i].ID(), res3.StreamObserverFactories[i].ID())
		assert.Equal(t, res1.AttemptTransforms[i].ID(), res2.AttemptTransforms[i].ID())
		assert.Equal(t, res1.AttemptTransforms[i].ID(), res3.AttemptTransforms[i].ID())
	}

	// Also test removeReasoningParticipants idempotence directly
	rem1 := removeReasoningParticipants(initial)
	rem2 := removeReasoningParticipants(rem1)
	assert.Equal(t, len(rem1.StreamObserverFactories), len(rem2.StreamObserverFactories))
	assert.Equal(t, len(rem1.AttemptTransforms), len(rem2.AttemptTransforms))
}

func TestReasoningCompression_FailBeforeMutate_CandidateUnmodified(t *testing.T) {
	t.Parallel()
	ps, reg, client, poller := setupReasoningTestServices(t, "ref-fail-mutate")

	initialSurface := featurebundle.MergedFeatureSurface{
		TerminalDecisionProvider: charTerminalProvider{id: "initial-provider"},
		StreamObserverFactories: []response.StreamObserverFactory{
			charObserverFactory{id: "custom-obs"},
		},
		AttemptTransforms: []request.AttemptTransform{
			charAttemptTransform{id: "custom-xform"},
		},
	}
	snapshot := initialSurface

	t.Run("appendReasoningCompressionBundle rejects conflicting provider leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		cand := initialSurface
		conflictingBundle := lipfeature.FeatureBundle{
			SchemaVersion:            lipfeature.SchemaVersionV1,
			TerminalDecisionProvider: charTerminalProvider{id: "conflicting-provider"},
		}

		res, err := appendReasoningCompressionBundle(cand, conflictingBundle)
		require.Error(t, err)
		assert.True(t, errors.Is(err, featurebundle.ErrTerminalDecisionProviderConflict))
		assert.Equal(t, featurebundle.MergedFeatureSurface{}, res, "failed composition must return empty surface")
		assert.Equal(t, snapshot, cand, "original candidate must remain unmodified")
	})

	t.Run("invalid config yaml returns error and empty surface leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		cand := initialSurface
		var badNode yaml.Node
		_ = yaml.Unmarshal([]byte("action: [invalid-unbalanced\n"), &badNode)

		badReg := lipsdk.Registration{
			ID:          reasoningpreservation.ID,
			FactoryKind: reasoningpreservation.ID,
			Kind:        lipsdk.PluginKindFeature,
			Enabled:     true,
			Config:      lipsdk.ConfigPayload{Node: badNode},
		}

		res, err := bindReasoningPreservationCompression(cand, ps, []lipsdk.Registration{badReg}, client, poller)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reasoningpreservation: config")
		assert.Equal(t, featurebundle.MergedFeatureSurface{}, res)
		assert.Equal(t, snapshot, cand, "original candidate must remain unmodified")
	})

	t.Run("nil background aux client/poller returns error and empty surface leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		cand := initialSurface
		res, err := bindReasoningPreservationCompression(cand, ps, []lipsdk.Registration{reg}, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BackgroundAux")
		assert.Equal(t, featurebundle.MergedFeatureSurface{}, res)
		assert.Equal(t, snapshot, cand, "original candidate must remain unmodified")
	})

	t.Run("missing egress policy returns error and empty surface leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		cand := initialSurface
		nodeMissingEgress := reasoningYAMLForTypedNil(t, "nonexistent-egress-ref")
		missingReg := lipsdk.Registration{
			ID:          reasoningpreservation.ID,
			FactoryKind: reasoningpreservation.ID,
			Kind:        lipsdk.PluginKindFeature,
			Enabled:     true,
			Config:      lipsdk.ConfigPayload{Node: nodeMissingEgress},
		}

		res, err := bindReasoningPreservationCompression(cand, ps, []lipsdk.Registration{missingReg}, client, poller)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "EgressPolicy")
		assert.Equal(t, featurebundle.MergedFeatureSurface{}, res)
		assert.Equal(t, snapshot, cand, "original candidate must remain unmodified")
	})

	t.Run("missing matcher resolver returns error and empty surface leaving candidate unmodified", func(t *testing.T) {
		t.Parallel()
		cand := initialSurface
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
		res, err := bindReasoningPreservationCompression(cand, psNoResolver, []lipsdk.Registration{reg}, client, poller)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SecretGuard MatcherResolver")
		assert.Equal(t, featurebundle.MergedFeatureSurface{}, res)
		assert.Equal(t, snapshot, cand, "original candidate must remain unmodified")
	})
}

func TestReasoningCompression_NilElementHandling(t *testing.T) {
	t.Parallel()
	ps, reg, client, poller := setupReasoningTestServices(t, "ref-nil-elem")

	reasoningObsID := reasoningpreservation.ID + "-observer"
	reasoningXformID := reasoningpreservation.ID + "-transform"

	t.Run("removeReasoningParticipants handles nil elements safely", func(t *testing.T) {
		t.Parallel()
		m := featurebundle.MergedFeatureSurface{
			StreamObserverFactories: []response.StreamObserverFactory{
				nil,
				charObserverFactory{id: "custom-obs"},
				nil,
				charObserverFactory{id: reasoningObsID},
				nil,
			},
			AttemptTransforms: []request.AttemptTransform{
				nil,
				charAttemptTransform{id: "custom-xform"},
				nil,
				charAttemptTransform{id: reasoningXformID},
				nil,
			},
		}

		cleaned := removeReasoningParticipants(m)
		// nils and custom elements are preserved; only reasoning elements removed
		require.Len(t, cleaned.StreamObserverFactories, 4)
		assert.Nil(t, cleaned.StreamObserverFactories[0])
		assert.Equal(t, "custom-obs", cleaned.StreamObserverFactories[1].ID())
		assert.Nil(t, cleaned.StreamObserverFactories[2])
		assert.Nil(t, cleaned.StreamObserverFactories[3])

		require.Len(t, cleaned.AttemptTransforms, 4)
		assert.Nil(t, cleaned.AttemptTransforms[0])
		assert.Equal(t, "custom-xform", cleaned.AttemptTransforms[1].ID())
		assert.Nil(t, cleaned.AttemptTransforms[2])
		assert.Nil(t, cleaned.AttemptTransforms[3])
	})

	t.Run("bindReasoningPreservationCompression handles nil elements safely", func(t *testing.T) {
		t.Parallel()
		m := featurebundle.MergedFeatureSurface{
			StreamObserverFactories: []response.StreamObserverFactory{
				nil,
				charObserverFactory{id: "custom-obs"},
				charObserverFactory{id: reasoningObsID},
			},
			AttemptTransforms: []request.AttemptTransform{
				nil,
				charAttemptTransform{id: "custom-xform"},
				charAttemptTransform{id: reasoningXformID},
			},
		}

		res, err := bindReasoningPreservationCompression(m, ps, []lipsdk.Registration{reg}, client, poller)
		require.NoError(t, err)
		require.Len(t, res.StreamObserverFactories, 3)
		assert.Nil(t, res.StreamObserverFactories[0])
		assert.Equal(t, "custom-obs", res.StreamObserverFactories[1].ID())
		assert.Equal(t, reasoningObsID, res.StreamObserverFactories[2].ID())

		require.Len(t, res.AttemptTransforms, 3)
		assert.Nil(t, res.AttemptTransforms[0])
		assert.Equal(t, "custom-xform", res.AttemptTransforms[1].ID())
		assert.Equal(t, reasoningXformID, res.AttemptTransforms[2].ID())
	})
}

func TestReasoningCompression_DisabledNoOp(t *testing.T) {
	t.Parallel()
	ps, reg, client, poller := setupReasoningTestServices(t, "ref-disabled")

	initial := featurebundle.MergedFeatureSurface{
		StreamObserverFactories: []response.StreamObserverFactory{
			charObserverFactory{id: "custom-obs"},
		},
		AttemptTransforms: []request.AttemptTransform{
			charAttemptTransform{id: "custom-xform"},
		},
	}

	t.Run("disabled registration is a no-op", func(t *testing.T) {
		t.Parallel()
		disabledReg := reg
		disabledReg.Enabled = false

		res, err := bindReasoningPreservationCompression(initial, ps, []lipsdk.Registration{disabledReg}, client, poller)
		require.NoError(t, err)
		assert.True(t, reflect.DeepEqual(initial, res))
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

		res, err := bindReasoningPreservationCompression(initial, ps, []lipsdk.Registration{disabledCfgReg}, client, poller)
		require.NoError(t, err)
		assert.True(t, reflect.DeepEqual(initial, res))
	})
}
