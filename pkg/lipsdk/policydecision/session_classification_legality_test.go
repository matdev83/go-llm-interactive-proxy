package policydecision_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

func TestSessionClassificationStage_AllowsOnlyFailOpenNonMutatingDecisions(t *testing.T) {
	t.Parallel()

	want := []policydecision.AllowedDecision{
		{Stage: feature.StageIDSessionClassification, Outcome: policydecision.OutcomeAllow, Effects: []policydecision.Effect{policydecision.EffectNone, policydecision.EffectAnnotate}},
		{Stage: feature.StageIDSessionClassification, Outcome: policydecision.OutcomeError, Effects: []policydecision.Effect{policydecision.EffectNone}},
	}
	require.Equal(t, want, policydecision.AllowedDecisionsForStage(feature.StageIDSessionClassification))
	require.True(t, policydecision.IsLegalPair(feature.StageIDSessionClassification, policydecision.OutcomeAllow, policydecision.EffectAnnotate))
	require.False(t, policydecision.IsLegalPair(feature.StageIDSessionClassification, policydecision.OutcomeDeny, policydecision.EffectNone))
	require.False(t, policydecision.IsLegalPair(feature.StageIDSessionClassification, policydecision.OutcomeAllow, policydecision.EffectMutate))
}
