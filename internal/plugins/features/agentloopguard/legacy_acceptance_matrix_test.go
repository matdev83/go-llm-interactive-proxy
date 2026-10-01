package agentloopguard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard/progress"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard/verifier"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// legacyConfigSource names an enabled-path YAML shape for the reusable legacy
// matrix. Omitted strategy is today's only enabled path; later explicit
// strategy: semantic_verifier should produce the same decisions.
type legacyConfigSource string

const legacyConfigOmittedStrategy legacyConfigSource = "omitted_strategy"

var legacyEnabledConfigSources = []legacyConfigSource{
	legacyConfigOmittedStrategy,
}

func legacyEnabledYAML(source legacyConfigSource, extra string) string {
	switch source {
	case legacyConfigOmittedStrategy:
		return "enabled: true\n" + extra
	default:
		panic("unknown legacy config source " + string(source))
	}
}

type legacyAcceptanceCase struct {
	name         string
	yamlExtra    string
	response     string
	auxErr       error
	omitAux      bool
	explicit     bool
	uncommitted  bool
	cause        terminaldecision.CandidateCause
	mutate       func(*terminaldecision.Input)
	wantKind     terminaldecision.DecisionKind
	wantReason   string
	wantCalls    int
	wantVerifier bool
	wantContinue bool
}

// legacyAcceptanceCases is the certified omitted-strategy decision matrix.
// Keep case names and expected outcomes stable so later explicit
// semantic_verifier YAML can reuse this table without rewriting fixtures.
var legacyAcceptanceCases = []legacyAcceptanceCase{
	{
		name:         "eligible clean stop uses detached verifier",
		response:     `{"kind":"INCOMPLETE","objective":"resume tests"}`,
		wantKind:     terminaldecision.DecisionContinue,
		wantReason:   progress.ReasonUnfinished,
		wantCalls:    1,
		wantVerifier: true,
		wantContinue: true,
	},
	{
		name:       "trusted explicit completion bypasses verifier",
		response:   `{"kind":"INCOMPLETE","objective":"must not be used"}`,
		explicit:   true,
		wantKind:   terminaldecision.DecisionAllowStop,
		wantReason: reasonExplicitComplete,
		wantCalls:  0,
	},
	{
		name:         "verify policy uses verifier for explicit completion",
		yamlExtra:    "explicit_completion_policy: verify\n",
		response:     `{"kind":"INCOMPLETE","objective":"resume tests"}`,
		explicit:     true,
		wantKind:     terminaldecision.DecisionContinue,
		wantReason:   progress.ReasonUnfinished,
		wantCalls:    1,
		wantVerifier: true,
		wantContinue: true,
	},
	{
		name:       "missing verifier is uncertain",
		omitAux:    true,
		wantKind:   terminaldecision.DecisionAllowStop,
		wantReason: progress.ReasonUncertain,
		wantCalls:  0,
	},
	{
		name:         "malformed verifier is uncertain",
		response:     "not-json",
		wantKind:     terminaldecision.DecisionAllowStop,
		wantReason:   progress.ReasonUncertain,
		wantCalls:    1,
		wantVerifier: true,
	},
	{
		name:         "uncertain verifier is uncertain",
		response:     `{"kind":"UNCERTAIN"}`,
		wantKind:     terminaldecision.DecisionAllowStop,
		wantReason:   progress.ReasonUncertain,
		wantCalls:    1,
		wantVerifier: true,
	},
	{
		name:         "verifier error is uncertain",
		auxErr:       errors.New("transport detail"),
		wantKind:     terminaldecision.DecisionAllowStop,
		wantReason:   progress.ReasonUncertain,
		wantCalls:    1,
		wantVerifier: true,
	},
	{
		name:         "verifier timeout error is uncertain",
		auxErr:       context.DeadlineExceeded,
		wantKind:     terminaldecision.DecisionAllowStop,
		wantReason:   progress.ReasonUncertain,
		wantCalls:    1,
		wantVerifier: true,
	},
	{
		name:         "complete without explicit completion stops after verifier",
		response:     `{"kind":"COMPLETE"}`,
		wantKind:     terminaldecision.DecisionAllowStop,
		wantReason:   progress.ReasonComplete,
		wantCalls:    1,
		wantVerifier: true,
	},
	{
		name:       "authoritative refusal skips verifier",
		cause:      terminaldecision.CandidateCauseRefusal,
		response:   `{"kind":"INCOMPLETE","objective":"must not be used"}`,
		wantKind:   terminaldecision.DecisionAllowStop,
		wantReason: "authoritative_candidate",
		wantCalls:  0,
	},
	{
		name:       "authoritative cancellation skips verifier",
		cause:      terminaldecision.CandidateCauseCancellation,
		response:   `{"kind":"INCOMPLETE","objective":"must not be used"}`,
		wantKind:   terminaldecision.DecisionAllowStop,
		wantReason: "authoritative_candidate",
		wantCalls:  0,
	},
	{
		name:       "authoritative content filter skips verifier",
		cause:      terminaldecision.CandidateCauseContentFilter,
		response:   `{"kind":"INCOMPLETE","objective":"must not be used"}`,
		wantKind:   terminaldecision.DecisionAllowStop,
		wantReason: "authoritative_candidate",
		wantCalls:  0,
	},
	{
		name:        "pre-output transport skips verifier",
		cause:       terminaldecision.CandidateCauseTransport,
		uncommitted: true,
		response:    `{"kind":"INCOMPLETE","objective":"must not be used"}`,
		wantKind:    terminaldecision.DecisionAllowStop,
		wantReason:  "pre_output_transport",
		wantCalls:   0,
	},
	{
		name:     "unsafe partial action skips verifier",
		cause:    terminaldecision.CandidateCauseProviderError,
		response: `{"kind":"INCOMPLETE","objective":"must not be used"}`,
		mutate: func(in *terminaldecision.Input) {
			in.Evidence.Actions[0] = terminaldecision.ActionFact{
				CallID: "call-unsafe",
				Kind:   lipapi.ItemKindToolCall,
				Status: lipapi.ItemStatusInProgress,
				Name:   "run_sensitive_tool",
			}
			in.Evidence.Actions[1] = terminaldecision.ActionFact{}
			in.Evidence.ActionCount = 1
		},
		wantKind:   terminaldecision.DecisionAllowStop,
		wantReason: "unsafe_action",
		wantCalls:  0,
	},
}

type legacyMatrixCollector struct {
	response       string
	err            error
	calls          int
	hadDeadline    bool
	deadlineRemain time.Duration
	req            auxiliary.Request
}

func (c *legacyMatrixCollector) Collect(ctx context.Context, req auxiliary.Request) (lipapi.Collected, error) {
	c.calls++
	c.req = req
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			c.hadDeadline = true
			c.deadlineRemain = time.Until(deadline)
		}
	}
	if c.err != nil {
		return lipapi.Collected{}, c.err
	}
	var out lipapi.Collected
	out.Text.WriteString(c.response)
	return out, nil
}

func (*legacyMatrixCollector) Stream(context.Context, auxiliary.Request) (lipapi.EventStream, error) {
	return nil, nil
}

func TestLegacyEnabledYAMLOmitsStrategyAndKeepsVerifierDefaults(t *testing.T) {
	t.Parallel()

	for _, source := range legacyEnabledConfigSources {
		source := source
		t.Run(string(source), func(t *testing.T) {
			t.Parallel()
			cfg := decodeLegacyYAML(t, legacyEnabledYAML(source, ""))
			require.True(t, cfg.Enabled)
			assert.Equal(t, DefaultVerifierRole, cfg.VerifierRole)
			assert.Equal(t, DefaultVerifierTimeoutSeconds, cfg.VerifierTimeoutSeconds)
			assert.Equal(t, DefaultMaxSemanticContinuations, cfg.MaxSemanticContinuations)
			assert.Equal(t, DefaultNoProgressLimit, cfg.NoProgressLimit)
			assert.Equal(t, ExplicitCompletionPolicyTrust, cfg.ExplicitCompletionPolicy)
		})
	}
}

func TestLegacyEnabledYAMLAcceptsExplicitSemanticVerifierSelector(t *testing.T) {
	t.Parallel()

	cfg, err := decodeLegacyYAMLErr(t, "enabled: true\nstrategy: semantic_verifier\n")
	require.NoError(t, err)
	assert.Equal(t, StrategySemanticVerifier, cfg.Strategy)
	assert.Equal(t, DefaultVerifierRole, cfg.VerifierRole)
	assert.Equal(t, DefaultVerifierTimeoutSeconds, cfg.VerifierTimeoutSeconds)
	assert.Equal(t, DefaultMaxSemanticContinuations, cfg.MaxSemanticContinuations)
	assert.Equal(t, DefaultNoProgressLimit, cfg.NoProgressLimit)
	assert.Equal(t, ExplicitCompletionPolicyTrust, cfg.ExplicitCompletionPolicy)
}

func TestLegacyAcceptanceMatrix(t *testing.T) {
	t.Parallel()

	for _, source := range legacyEnabledConfigSources {
		source := source
		t.Run(string(source), func(t *testing.T) {
			t.Parallel()
			for _, tc := range legacyAcceptanceCases {
				tc := tc
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					runLegacyAcceptanceCase(t, source, tc)
				})
			}
		})
	}
}

func TestLegacyAcceptanceMatrixProgressAndBudgetCaps(t *testing.T) {
	t.Parallel()

	for _, source := range legacyEnabledConfigSources {
		source := source
		t.Run(string(source), func(t *testing.T) {
			t.Parallel()

			t.Run("no-progress", func(t *testing.T) {
				t.Parallel()
				prov := NewProvider(decodeLegacyYAML(t, legacyEnabledYAML(source, "max_semantic_continuations: 5\nno_progress_limit: 2\n")))
				in := semanticProviderInput()
				in.Policy.MaxContinuationAttempts = 5
				collector := &providerSemanticCollector{responses: []string{
					`{"kind":"INCOMPLETE","objective":"resume tests"}`,
					`{"kind":"INCOMPLETE","objective":"resume tests"}`,
					`{"kind":"INCOMPLETE","objective":"resume tests"}`,
				}}
				in.Auxiliary = collector

				first := mustDecide(t, prov, in)
				require.Equal(t, terminaldecision.DecisionContinue, first.Kind)
				require.NotNil(t, first.Continue)

				secondIn := in
				secondIn.Continuation.Attempt = 2
				secondIn.Evidence.Lineage.Attempt = 2
				secondIn.Evidence.Lineage.ProgressRef = first.Continue.ControlRef
				second := mustDecide(t, prov, secondIn)
				require.Equal(t, terminaldecision.DecisionContinue, second.Kind)
				require.NotNil(t, second.Continue)

				thirdIn := secondIn
				thirdIn.Continuation.Attempt = 3
				thirdIn.Evidence.Lineage.Attempt = 3
				thirdIn.Evidence.Lineage.ProgressRef = second.Continue.ControlRef
				third := mustDecide(t, prov, thirdIn)
				assert.Equal(t, terminaldecision.DecisionAllowStop, third.Kind)
				assert.Equal(t, progress.ReasonNoProgress, third.ReasonCode)
				assert.Nil(t, third.Continue)
			})

			t.Run("semantic-budget", func(t *testing.T) {
				t.Parallel()
				prov := NewProvider(decodeLegacyYAML(t, legacyEnabledYAML(source, "max_semantic_continuations: 3\nno_progress_limit: 64\n")))
				in := semanticProviderInput()
				in.Policy.MaxContinuationAttempts = 3
				in.Auxiliary = &providerSemanticCollector{responses: []string{
					`{"kind":"INCOMPLETE","objective":"resume tests"}`,
					`{"kind":"INCOMPLETE","objective":"resume tests"}`,
					`{"kind":"INCOMPLETE","objective":"resume tests"}`,
				}}

				first := mustDecide(t, prov, in)
				require.Equal(t, terminaldecision.DecisionContinue, first.Kind)
				require.NotNil(t, first.Continue)

				secondIn := in
				secondIn.Continuation.Attempt = 2
				secondIn.Evidence.Lineage.Attempt = 2
				secondIn.Evidence.Lineage.ProgressRef = first.Continue.ControlRef
				second := mustDecide(t, prov, secondIn)
				require.Equal(t, terminaldecision.DecisionContinue, second.Kind)
				require.NotNil(t, second.Continue)

				thirdIn := secondIn
				thirdIn.Continuation.Attempt = 3
				thirdIn.Evidence.Lineage.Attempt = 3
				thirdIn.Evidence.Lineage.ProgressRef = second.Continue.ControlRef
				third := mustDecide(t, prov, thirdIn)
				assert.Equal(t, terminaldecision.DecisionAllowStop, third.Kind)
				assert.Equal(t, progress.ReasonBudgetExhausted, third.ReasonCode)
				assert.Nil(t, third.Continue)
			})
		})
	}
}

func TestLegacyEnabledSnapshotPinsProviderAndSurvivesInvalidCandidate(t *testing.T) {
	t.Parallel()

	first, err := composeLegacyALGBundle(t, legacyEnabledYAML(legacyConfigOmittedStrategy, "max_semantic_continuations: 2\nno_progress_limit: 64\n"))
	require.NoError(t, err)
	require.Equal(t, []string{lipfeature.PlaneTerminalDecisionProvider.ID}, occupiedStandardPlanes(t, first.PlaneSet))
	firstProv := lipfeature.Get(first.PlaneSet, lipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, firstProv)

	second, err := composeLegacyALGBundle(t, legacyEnabledYAML(legacyConfigOmittedStrategy, "max_semantic_continuations: 4\nno_progress_limit: 64\n"))
	require.NoError(t, err)
	secondProv := lipfeature.Get(second.PlaneSet, lipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, secondProv)

	assertProviderTripsSemanticCap(t, firstProv, 2)
	assertProviderTripsSemanticCap(t, secondProv, 4)

	cloned := first.PlaneSet.Clone()
	fromClone := lipfeature.Get(cloned, lipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, fromClone)
	assertProviderTripsSemanticCap(t, fromClone, 2)

	_, err = composeLegacyALGBundle(t, "enabled: true\nmax_semantic_continuations: 0\n")
	require.Error(t, err)

	stillFirst := lipfeature.Get(first.PlaneSet, lipfeature.PlaneTerminalDecisionProvider)
	stillSecond := lipfeature.Get(second.PlaneSet, lipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, stillFirst)
	require.NotNil(t, stillSecond)
	assertProviderTripsSemanticCap(t, stillFirst, 2)
	assertProviderTripsSemanticCap(t, stillSecond, 4)
}

func TestLegacyDisabledAndEmptyPlanesContributeNoProvider(t *testing.T) {
	t.Parallel()

	disabled, err := composeLegacyALGBundle(t, "enabled: false\n")
	require.NoError(t, err)
	assert.Empty(t, occupiedStandardPlanes(t, disabled.PlaneSet))
	assert.Nil(t, lipfeature.Get(disabled.PlaneSet, lipfeature.PlaneTerminalDecisionProvider))

	empty := lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1}
	assert.Nil(t, lipfeature.Get(empty.PlaneSet, lipfeature.PlaneTerminalDecisionProvider))
}

func runLegacyAcceptanceCase(t *testing.T, source legacyConfigSource, tc legacyAcceptanceCase) {
	t.Helper()

	cfg := decodeLegacyYAML(t, legacyEnabledYAML(source, tc.yamlExtra))
	prov := NewProvider(cfg)
	cause := tc.cause
	if cause == "" {
		cause = terminaldecision.CandidateCauseNormal
	}
	in := semanticProviderInput()
	in.Candidate.Cause = cause
	in.Evidence.ExplicitCompletion = tc.explicit
	if tc.uncommitted {
		in.Candidate.OutputCommitted = false
	}
	if tc.mutate != nil {
		tc.mutate(&in)
	}

	var collector *legacyMatrixCollector
	if !tc.omitAux {
		collector = &legacyMatrixCollector{response: tc.response, err: tc.auxErr}
		in.Auxiliary = collector
	}

	decision, err := prov.Decide(context.Background(), in)
	require.NoError(t, err)
	require.NoError(t, decision.Validate())
	assert.Equal(t, tc.wantKind, decision.Kind)
	assert.Equal(t, tc.wantReason, decision.ReasonCode)
	if tc.wantContinue {
		require.NotNil(t, decision.Continue)
		require.NoError(t, decision.Continue.Validate())
		assert.Equal(t, terminaldecision.DecisionContinue, decision.Kind)
	} else {
		assert.Nil(t, decision.Continue)
	}

	calls := 0
	if collector != nil {
		calls = collector.calls
	}
	assert.Equal(t, tc.wantCalls, calls)
	if tc.wantVerifier {
		require.NotNil(t, collector)
		assert.True(t, collector.hadDeadline, "current enabled path must bound the verifier with a deadline")
		assert.InDelta(t, cfg.VerifierTimeout.Seconds(), collector.deadlineRemain.Seconds(), 1.0,
			"Collect deadline must be VerifierTimeout, not the platform Input.Deadline")
		assert.Equal(t, DefaultVerifierRole, collector.req.Role)
		assert.Equal(t, verifier.VisibilityPrivate, collector.req.Visibility)
		assert.Equal(t, auxiliary.SessionModeDetached, collector.req.SessionMode)
		require.Equal(t, []string{verifier.RecursionPluginID}, collector.req.DisablePlugins)
	}
}

func assertProviderTripsSemanticCap(t *testing.T, prov terminaldecision.Provider, cap uint8) {
	t.Helper()
	require.Greater(t, cap, uint8(1))

	in := semanticProviderInput()
	in.Policy.MaxContinuationAttempts = 8
	responses := make([]string, int(cap)+1)
	for i := range responses {
		responses[i] = `{"kind":"INCOMPLETE","objective":"resume tests"}`
	}
	in.Auxiliary = &providerSemanticCollector{responses: responses}

	var prev terminaldecision.Decision
	for attempt := uint8(1); attempt < cap; attempt++ {
		if attempt > 1 {
			in.Continuation.Attempt = attempt
			in.Evidence.Lineage.Attempt = attempt
			require.NotNil(t, prev.Continue)
			in.Evidence.Lineage.ProgressRef = prev.Continue.ControlRef
		}
		decision := mustDecide(t, prov, in)
		require.Equal(t, terminaldecision.DecisionContinue, decision.Kind, "attempt %d under cap %d should continue", attempt, cap)
		require.NotNil(t, decision.Continue)
		prev = decision
	}

	in.Continuation.Attempt = cap
	in.Evidence.Lineage.Attempt = cap
	require.NotNil(t, prev.Continue)
	in.Evidence.Lineage.ProgressRef = prev.Continue.ControlRef
	last := mustDecide(t, prov, in)
	assert.Equal(t, terminaldecision.DecisionAllowStop, last.Kind)
	assert.Equal(t, progress.ReasonBudgetExhausted, last.ReasonCode)
	assert.Nil(t, last.Continue)
}

func mustDecide(t *testing.T, prov terminaldecision.Provider, in terminaldecision.Input) terminaldecision.Decision {
	t.Helper()
	decision, err := prov.Decide(context.Background(), in)
	require.NoError(t, err)
	require.NoError(t, decision.Validate())
	return decision
}

func decodeLegacyYAML(t *testing.T, raw string) Config {
	t.Helper()
	cfg, err := decodeLegacyYAMLErr(t, raw)
	require.NoError(t, err)
	return cfg
}

func decodeLegacyYAMLErr(t *testing.T, raw string) (Config, error) {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		return Config{}, err
	}
	return DecodeConfig(node)
}

func composeLegacyALGBundle(t *testing.T, raw string) (lipfeature.FeatureBundle, error) {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	cfg, err := DecodeConfig(node)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	if !cfg.Enabled {
		return lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1}, nil
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, ID, NewProvider(cfg)); err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func occupiedStandardPlanes(t *testing.T, set lipfeature.FrozenPlaneSet) []string {
	t.Helper()
	cs := set.ToContributions()
	ids := make([]string, 0, 1)
	for _, plane := range lipfeature.StandardPlanes {
		if cs.Has(plane.PlaneID()) {
			ids = append(ids, plane.PlaneID())
		}
	}
	return ids
}
