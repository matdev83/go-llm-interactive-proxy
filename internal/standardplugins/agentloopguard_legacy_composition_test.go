package standardplugins

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/agentloopguard/progress"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestStandardBundle_AgentLoopGuardOmittedStrategyContributesTerminalProviderOnly(t *testing.T) {
	t.Parallel()

	bundle := mustBuildALGBundle(t, "enabled: true\n")
	require.Equal(t, []string{lipfeature.PlaneTerminalDecisionProvider.ID}, occupiedStandardPlanes(t, bundle.PlaneSet))
	prov := lipfeature.Get(bundle.PlaneSet, lipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, prov)
	id, err := terminaldecision.ProviderIdentity(prov)
	require.NoError(t, err)
	assert.Equal(t, agentloopguard.ID, id)
}

func TestStandardBundle_AgentLoopGuardDisabledAndWithdrawnLeaveNoProvider(t *testing.T) {
	t.Parallel()

	t.Run("disabled", func(t *testing.T) {
		t.Parallel()
		bundle := mustBuildALGBundle(t, "enabled: false\n")
		assert.Empty(t, occupiedStandardPlanes(t, bundle.PlaneSet))
		assert.Nil(t, lipfeature.Get(bundle.PlaneSet, lipfeature.PlaneTerminalDecisionProvider))
	})

	t.Run("withdrawn-registry", func(t *testing.T) {
		t.Parallel()
		reg := pluginreg.NewRegistry()
		stock := StandardBundle()
		withoutALG := Bundle{Features: make([]FeatureRegistration, 0, len(stock.Features))}
		for _, feature := range stock.Features {
			if feature.ID == agentloopguard.ID {
				continue
			}
			withoutALG.Features = append(withoutALG.Features, feature)
		}
		require.NoError(t, InstallBundleOn(reg, withoutALG))

		var node yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte("enabled: true\n"), &node))
		_, err := reg.BuildFeatureBundle(agentloopguard.ID, node)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown feature plugin")

		empty := lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1}
		assert.Nil(t, lipfeature.Get(empty.PlaneSet, lipfeature.PlaneTerminalDecisionProvider))
	})
}

func TestStandardBundle_AgentLoopGuardInvalidCandidateLeavesLastGoodBundle(t *testing.T) {
	t.Parallel()

	reg := testRegistryWithStdBundle(t)
	first := mustBuildALGBundleOn(t, reg, "enabled: true\nmax_semantic_continuations: 2\nno_progress_limit: 64\n")
	firstProv := lipfeature.Get(first.PlaneSet, lipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, firstProv)

	second := mustBuildALGBundleOn(t, reg, "enabled: true\nmax_semantic_continuations: 4\nno_progress_limit: 64\n")
	secondProv := lipfeature.Get(second.PlaneSet, lipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, secondProv)

	assertALGProviderTripsSemanticCap(t, firstProv, 2)
	assertALGProviderTripsSemanticCap(t, secondProv, 4)

	var invalid yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("enabled: true\nmax_semantic_continuations: 0\n"), &invalid))
	_, err := reg.BuildFeatureBundle(agentloopguard.ID, invalid)
	require.Error(t, err)

	stillFirst := lipfeature.Get(first.PlaneSet, lipfeature.PlaneTerminalDecisionProvider)
	stillSecond := lipfeature.Get(second.PlaneSet, lipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, stillFirst)
	require.NotNil(t, stillSecond)
	assertALGProviderTripsSemanticCap(t, stillFirst, 2)
	assertALGProviderTripsSemanticCap(t, stillSecond, 4)
	assert.Equal(t, []string{lipfeature.PlaneTerminalDecisionProvider.ID}, occupiedStandardPlanes(t, first.PlaneSet))
	assert.Equal(t, []string{lipfeature.PlaneTerminalDecisionProvider.ID}, occupiedStandardPlanes(t, second.PlaneSet))
}

func mustBuildALGBundle(t *testing.T, raw string) lipfeature.FeatureBundle {
	t.Helper()
	return mustBuildALGBundleOn(t, testRegistryWithStdBundle(t), raw)
}

func mustBuildALGBundleOn(t *testing.T, reg *pluginreg.Registry, raw string) lipfeature.FeatureBundle {
	t.Helper()
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(raw), &node))
	bundle, err := reg.BuildFeatureBundle(agentloopguard.ID, node)
	require.NoError(t, err)
	require.NoError(t, bundle.Validate())
	return bundle
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

type algPinCollector struct {
	responses []string
	calls     int
}

func (c *algPinCollector) Collect(context.Context, auxiliary.Request) (lipapi.Collected, error) {
	var out lipapi.Collected
	if c.calls < len(c.responses) {
		out.Text.WriteString(c.responses[c.calls])
	}
	c.calls++
	return out, nil
}

func (*algPinCollector) Stream(context.Context, auxiliary.Request) (lipapi.EventStream, error) {
	return nil, nil
}

func algPinnedSemanticInput() terminaldecision.Input {
	return terminaldecision.Input{
		Candidate: terminaldecision.CanonicalTerminalCandidate{
			Cause:           terminaldecision.CandidateCauseNormal,
			Reference:       "candidate-1",
			OutputCommitted: true,
		},
		Request: terminaldecision.RequestIdentity{
			RequestID: "request-1",
			TraceID:   "trace-1",
			ALegID:    "a-leg-1",
			BLegID:    "b-leg-1",
		},
		Policy: terminaldecision.PolicySnapshot{
			Revision:                "policy-1",
			MaxContinuationAttempts: 8,
		},
		Continuation: terminaldecision.ContinuationEvidence{
			TrajectoryRef: "trajectory-1",
			Attempt:       1,
		},
		Evidence: terminaldecision.Evidence{
			Objective:     "finish the requested change",
			RecentText:    "run the focused tests",
			CandidateText: "the implementation is ready",
			Actions: [terminaldecision.MaxEvidenceActions]terminaldecision.ActionFact{
				{
					ItemID: "item-1",
					CallID: "call-1",
					Kind:   lipapi.ItemKindToolResult,
					Status: lipapi.ItemStatusCompleted,
					Name:   "go_test",
				},
				{
					ItemID: "item-2",
					Kind:   lipapi.ItemKindMessage,
					Status: lipapi.ItemStatusInProgress,
				},
			},
			ActionCount: 2,
			Lineage: terminaldecision.EvidenceLineage{
				TrajectoryRef: "trajectory-1",
				ParentRef:     "trajectory-0",
				Attempt:       1,
			},
		},
		Deadline: time.Date(2035, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
}

func assertALGProviderTripsSemanticCap(t *testing.T, prov terminaldecision.Provider, cap uint8) {
	t.Helper()
	require.Greater(t, cap, uint8(1))

	in := algPinnedSemanticInput()
	responses := make([]string, int(cap)+1)
	for i := range responses {
		responses[i] = `{"kind":"INCOMPLETE","objective":"resume tests"}`
	}
	in.Auxiliary = &algPinCollector{responses: responses}

	var prev terminaldecision.Decision
	for attempt := uint8(1); attempt < cap; attempt++ {
		if attempt > 1 {
			in.Continuation.Attempt = attempt
			in.Evidence.Lineage.Attempt = attempt
			require.NotNil(t, prev.Continue)
			in.Evidence.Lineage.ProgressRef = prev.Continue.ControlRef
		}
		decision, err := prov.Decide(context.Background(), in)
		require.NoError(t, err)
		require.NoError(t, decision.Validate())
		require.Equal(t, terminaldecision.DecisionContinue, decision.Kind, "attempt %d under cap %d should continue", attempt, cap)
		require.NotNil(t, decision.Continue)
		prev = decision
	}

	in.Continuation.Attempt = cap
	in.Evidence.Lineage.Attempt = cap
	require.NotNil(t, prev.Continue)
	in.Evidence.Lineage.ProgressRef = prev.Continue.ControlRef
	last, err := prov.Decide(context.Background(), in)
	require.NoError(t, err)
	require.NoError(t, last.Validate())
	assert.Equal(t, terminaldecision.DecisionAllowStop, last.Kind)
	assert.Equal(t, progress.ReasonBudgetExhausted, last.ReasonCode)
	assert.Nil(t, last.Continue)
}
