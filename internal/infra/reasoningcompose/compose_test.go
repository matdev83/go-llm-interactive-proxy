package reasoningcompose_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactioncompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/reasoningcompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type testEgressPolicy struct {
	version string
}

func (p testEgressPolicy) Decide(context.Context, reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: p.version}, nil
}

type testMatcherResolver struct{}

func (testMatcherResolver) Resolve(context.Context) (sdk.Matcher, error) {
	return testMatcher{}, nil
}

type testMatcher struct{}

func (testMatcher) ScanBytes(_ context.Context, _ []byte) ([]sdk.Finding, error)  { return nil, nil }
func (testMatcher) ScanString(_ context.Context, _ string) ([]sdk.Finding, error) { return nil, nil }
func (testMatcher) RedactBytes(_ context.Context, b []byte) ([]byte, []sdk.Finding, error) {
	return b, nil, nil
}
func (testMatcher) RedactString(_ context.Context, s string) (string, []sdk.Finding, error) {
	return s, nil, nil
}

type testRunner struct {
	id string
}

func (r testRunner) Execute(context.Context, *lipapi.Call) (lipapi.EventStream, error) {
	return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "ok-" + r.id}, {Kind: lipapi.EventResponseFinished}}), nil
}

type typedNilClient struct{}

func (t *typedNilClient) SubmitCollect(context.Context, auxiliary.Request, auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return "", nil
}
func (t *typedNilClient) Await(context.Context, auxiliary.JobID) (lipapi.Collected, error) {
	return lipapi.Collected{}, nil
}
func (t *typedNilClient) Forget(auxiliary.JobID) {}
func (t *typedNilClient) Poll(context.Context, auxiliary.JobID) (auxiliary.PollResult, error) {
	return auxiliary.PollResult{}, nil
}

type typedNilEgress struct{}

func (t *typedNilEgress) Decide(context.Context, reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: "v1"}, nil
}

type typedNilResolver struct{}

func (t *typedNilResolver) Resolve(context.Context) (sdk.Matcher, error) { return nil, nil }

func testReasoningYAML(t *testing.T, enabled bool, egressRef string) yaml.Node {
	t.Helper()
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
`
	if enabled {
		raw += `
compression:
  enabled: true
  mode: shadow
  route: test-route
  timeout: 5s
  max_input_tokens: 10000
  max_input_bytes: 100000
  max_output_tokens: 1000
  max_output_bytes: 100000
  max_surrogate_bytes: 50000
  min_source_bytes: 100
  min_saved_bytes: 50
  min_savings_ratio: 0.5
  max_pending_per_session: 10
  max_surrogate_bytes_per_session: 100000
  max_pending_total: 100
  max_surrogate_bytes_total: 1000000
  egress_policy_ref: ` + egressRef + `
`
	} else {
		raw += `
compression:
  enabled: false
`
	}
	var n yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(raw), &n))
	return n
}

func setupTestScheduler(t *testing.T) (auxiliary.BackgroundClient, auxiliary.BackgroundPoller) {
	t.Helper()
	scheduler, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner {
		return testRunner{id: "sched-runner"}
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 4, MaxResults: 10})
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheduler.Close() })

	genRunner := compactioncompose.NewGenerationExecutorRunner()
	bound := scheduler.BindRunner(genRunner)
	poller, ok := bound.(auxiliary.BackgroundPoller)
	require.True(t, ok)
	return bound, poller
}

func TestReasoningCompression_OptionsPrecedence(t *testing.T) {
	t.Parallel()

	prodPolicy := testEgressPolicy{version: "prod"}
	testPolicy := testEgressPolicy{version: "test"}
	sharedPolicy := testEgressPolicy{version: "prod-override"}

	prod := reasoningcompose.Options{
		EgressPolicies: map[string]reasoningpreservation.EgressPolicy{
			"prod-only": prodPolicy,
			"shared":    sharedPolicy,
		},
		MatcherResolver: testMatcherResolver{},
	}
	test := reasoningcompose.Options{
		EgressPolicies: map[string]reasoningpreservation.EgressPolicy{
			"test-only": testPolicy,
			"shared":    testPolicy,
		},
	}

	merged := reasoningcompose.ComposeOptions(prod, test)
	assert.Equal(t, 3, len(merged.EgressPolicies))
	assert.Equal(t, prodPolicy, merged.EgressPolicies["prod-only"])
	assert.Equal(t, testPolicy, merged.EgressPolicies["test-only"])
	assert.Equal(t, sharedPolicy, merged.EgressPolicies["shared"])
	assert.NotNil(t, merged.MatcherResolver)

	// Fallback to test matcher resolver when prod is nil capability
	var nilResolver *typedNilResolver
	prodWithNil := reasoningcompose.Options{
		MatcherResolver: nilResolver,
	}
	testWithResolver := reasoningcompose.Options{
		MatcherResolver: testMatcherResolver{},
	}
	mergedFallback := reasoningcompose.ComposeOptions(prodWithNil, testWithResolver)
	assert.NotNil(t, mergedFallback.MatcherResolver)
}

func TestReasoningCompression_ValidatePrerequisites(t *testing.T) {
	t.Parallel()

	client, poller := setupTestScheduler(t)
	node := testReasoningYAML(t, true, "my-egress")
	reg := lipsdk.Registration{
		ID:          reasoningpreservation.ID,
		FactoryKind: reasoningpreservation.ID,
		Kind:        lipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      lipsdk.ConfigPayload{Node: node},
	}

	t.Run("fails when BackgroundAux is nil", func(t *testing.T) {
		t.Parallel()
		in := reasoningcompose.GenerationInput{
			Registrations: []lipsdk.Registration{reg},
			Client:        nil,
			Poller:        nil,
			Options: reasoningcompose.Options{
				EgressPolicies:  map[string]reasoningpreservation.EgressPolicy{"my-egress": testEgressPolicy{}},
				MatcherResolver: testMatcherResolver{},
			},
		}
		err := reasoningcompose.Validate(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BackgroundAux")
	})

	t.Run("fails when client or poller is typed nil", func(t *testing.T) {
		t.Parallel()
		var nilClient *typedNilClient
		var c auxiliary.BackgroundClient = nilClient
		var p auxiliary.BackgroundPoller = nilClient
		in := reasoningcompose.GenerationInput{
			Registrations: []lipsdk.Registration{reg},
			Client:        c,
			Poller:        p,
			Options: reasoningcompose.Options{
				EgressPolicies:  map[string]reasoningpreservation.EgressPolicy{"my-egress": testEgressPolicy{}},
				MatcherResolver: testMatcherResolver{},
			},
		}
		err := reasoningcompose.Validate(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BackgroundAux")
	})

	t.Run("fails when egress policy is missing", func(t *testing.T) {
		t.Parallel()
		in := reasoningcompose.GenerationInput{
			Registrations: []lipsdk.Registration{reg},
			Client:        client,
			Poller:        poller,
			Options: reasoningcompose.Options{
				EgressPolicies:  map[string]reasoningpreservation.EgressPolicy{"other-egress": testEgressPolicy{}},
				MatcherResolver: testMatcherResolver{},
			},
		}
		err := reasoningcompose.Validate(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `trusted EgressPolicy for "my-egress"`)
	})

	t.Run("fails when egress policy is typed nil", func(t *testing.T) {
		t.Parallel()
		var nilPolicy *typedNilEgress
		in := reasoningcompose.GenerationInput{
			Registrations: []lipsdk.Registration{reg},
			Client:        client,
			Poller:        poller,
			Options: reasoningcompose.Options{
				EgressPolicies:  map[string]reasoningpreservation.EgressPolicy{"my-egress": nilPolicy},
				MatcherResolver: testMatcherResolver{},
			},
		}
		err := reasoningcompose.Validate(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `trusted EgressPolicy for "my-egress"`)
	})

	t.Run("fails when MatcherResolver is nil or typed nil", func(t *testing.T) {
		t.Parallel()
		var nilResolver *typedNilResolver
		in := reasoningcompose.GenerationInput{
			Registrations: []lipsdk.Registration{reg},
			Client:        client,
			Poller:        poller,
			Options: reasoningcompose.Options{
				EgressPolicies:  map[string]reasoningpreservation.EgressPolicy{"my-egress": testEgressPolicy{}},
				MatcherResolver: nilResolver,
			},
		}
		err := reasoningcompose.Validate(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SecretGuard MatcherResolver")
	})

	t.Run("succeeds when compression is disabled", func(t *testing.T) {
		t.Parallel()
		disabledNode := testReasoningYAML(t, false, "")
		disabledReg := lipsdk.Registration{
			ID:          reasoningpreservation.ID,
			FactoryKind: reasoningpreservation.ID,
			Kind:        lipsdk.PluginKindFeature,
			Enabled:     true,
			Config:      lipsdk.ConfigPayload{Node: disabledNode},
		}
		in := reasoningcompose.GenerationInput{
			Registrations: []lipsdk.Registration{disabledReg},
		}
		require.NoError(t, reasoningcompose.Validate(in))
	})
}

func TestReasoningCompression_BindReplaceByIdentityAndIdempotence(t *testing.T) {
	t.Parallel()

	client, poller := setupTestScheduler(t)
	node := testReasoningYAML(t, true, "my-egress")
	reg := lipsdk.Registration{
		ID:          reasoningpreservation.ID,
		FactoryKind: reasoningpreservation.ID,
		Kind:        lipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      lipsdk.ConfigPayload{Node: node},
	}
	opts := reasoningcompose.Options{
		EgressPolicies:  map[string]reasoningpreservation.EgressPolicy{"my-egress": testEgressPolicy{version: "v1"}},
		MatcherResolver: testMatcherResolver{},
	}

	cs := lipfeature.NewContributionSet()
	initialGen := featurebundle.GeneratedMergeSurface{
		Frozen: cs.Freeze(),
	}

	in := reasoningcompose.GenerationInput{
		Registrations: []lipsdk.Registration{reg},
		Client:        client,
		Poller:        poller,
		Options:       opts,
	}

	resGen1, err := reasoningcompose.Bind(initialGen, in)
	require.NoError(t, err)

	obs1 := lipfeature.Get(resGen1.Frozen, lipfeature.PlaneStreamObserverFactories)
	xforms1 := lipfeature.Get(resGen1.Frozen, lipfeature.PlaneAttemptTransforms)
	require.Len(t, obs1, 1)
	require.Len(t, xforms1, 1)
	assert.Equal(t, reasoningpreservation.ID+"-observer", obs1[0].ID())
	assert.Equal(t, reasoningpreservation.ID+"-transform", xforms1[0].ID())

	// Idempotence: binding again should replace, not duplicate
	resGen2, err := reasoningcompose.Bind(resGen1, in)
	require.NoError(t, err)

	obs2 := lipfeature.Get(resGen2.Frozen, lipfeature.PlaneStreamObserverFactories)
	xforms2 := lipfeature.Get(resGen2.Frozen, lipfeature.PlaneAttemptTransforms)
	assert.Len(t, obs2, 1)
	assert.Len(t, xforms2, 1)
}

func TestReasoningCompression_BindFailBeforeMutate_CandidateUnmodified(t *testing.T) {
	t.Parallel()

	client, poller := setupTestScheduler(t)
	node := testReasoningYAML(t, true, "missing-ref")
	reg := lipsdk.Registration{
		ID:          reasoningpreservation.ID,
		FactoryKind: reasoningpreservation.ID,
		Kind:        lipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      lipsdk.ConfigPayload{Node: node},
	}

	cs := lipfeature.NewContributionSet()
	candGen := featurebundle.GeneratedMergeSurface{
		Frozen: cs.Freeze(),
	}

	in := reasoningcompose.GenerationInput{
		Registrations: []lipsdk.Registration{reg},
		Client:        client,
		Poller:        poller,
		Options:       reasoningcompose.Options{}, // missing egress policy
	}

	resGen, err := reasoningcompose.Bind(candGen, in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trusted EgressPolicy")
	assert.True(t, resGen.Frozen.IsZero(), "failed bind must return zero surface")
}

func TestReasoningCompression_BindDisabledNoOp(t *testing.T) {
	t.Parallel()

	client, poller := setupTestScheduler(t)
	disabledNode := testReasoningYAML(t, false, "")
	reg := lipsdk.Registration{
		ID:          reasoningpreservation.ID,
		FactoryKind: reasoningpreservation.ID,
		Kind:        lipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      lipsdk.ConfigPayload{Node: disabledNode},
	}

	cs := lipfeature.NewContributionSet()
	candGen := featurebundle.GeneratedMergeSurface{
		Frozen: cs.Freeze(),
	}

	in := reasoningcompose.GenerationInput{
		Registrations: []lipsdk.Registration{reg},
		Client:        client,
		Poller:        poller,
	}

	resGen, err := reasoningcompose.Bind(candGen, in)
	require.NoError(t, err)
	assert.Equal(t, candGen, resGen)
}
