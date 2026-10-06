package secretguard

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildGenerationServices_BuildsIndependentImmutableScanner(t *testing.T) {
	t.Parallel()

	policy := DetectorPolicy{
		BetterLeaks: BetterLeaksPolicy{
			Enabled:           true,
			MinimumConfidence: DefaultBetterLeaksConfidence,
			MaxDecodeDepth:    DefaultBetterLeaksDecodeDepth,
			Workers:           1,
			MaxFindings:       DefaultBetterLeaksMaxFindings,
		},
	}
	source := engine.NewDisabledSource()
	first, err := BuildGenerationServices(policy, source)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.NotNil(t, first.betterLeaks)

	second, err := BuildGenerationServices(policy, source)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.NotNil(t, second.betterLeaks)
	assert.NotSame(t, first.betterLeaks, second.betterLeaks, "each generation must own its scanner handle")

	facts := first.DetectorFacts()
	assert.Equal(t, betterLeaksPinnedVersion, facts.Version)
	assert.NotEmpty(t, facts.ConfigHash)
	assert.NotEmpty(t, facts.RuleInventoryHash)
	assert.Greater(t, facts.ActiveRuleCount, 0)
	assert.Equal(t, DefaultBetterLeaksConfidence, facts.MinimumConfidence)
	assert.Equal(t, DefaultBetterLeaksDecodeDepth, facts.MaxDecodeDepth)
	assert.Equal(t, 1, facts.Workers)
	assert.NotEmpty(t, facts.RuleIDs)

	facts.RuleIDs[0] = "mutated"
	assert.NotEqual(t, "mutated", first.DetectorFacts().RuleIDs[0], "policy facts must be defensively copied")
}

func TestBuildGenerationServices_DisabledSkipsScannerConstruction(t *testing.T) {
	t.Parallel()

	services, err := BuildGenerationServices(DetectorPolicy{}, engine.NewDisabledSource())
	require.NoError(t, err)
	require.NotNil(t, services)
	assert.Nil(t, services.betterLeaks)
	assert.Zero(t, services.DetectorFacts())
}
