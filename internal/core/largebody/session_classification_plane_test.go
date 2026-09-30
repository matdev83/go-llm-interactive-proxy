package largebody_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"
)

type eligibleSessionClassifier struct{ id string }

func (c eligibleSessionClassifier) ID() string { return c.id }

func (eligibleSessionClassifier) Classify(context.Context, sessionclassification.Input) (session.Classification, error) {
	return session.Classification{}, nil
}

func TestSessionClassifierPlane_OccupiedMetadataOnlyPlaneStaysWireEligible(t *testing.T) {
	t.Parallel()

	contributions := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(contributions, feature.PlaneSessionClassifier, "session-classification", sessionclassification.Classifier(eligibleSessionClassifier{id: "session-classification"})))
	frozen := contributions.Freeze()
	require.NotNil(t, feature.Get(frozen, feature.PlaneSessionClassifier))

	input := occupy35Plane(eligible35Input("gen-session-classification"), feature.PlaneSessionClassifier.ID)
	summary, err := largebody.CompileWireEligibilitySummary(input, testEligibilityBudget)
	require.NoError(t, err)
	require.False(t, summary.HasStaticBlocker())

	disposition, reason := summary.StaticDisposition(largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   "gen-session-classification",
		ThresholdBytes: 1,
		ContentLength:  2,
	})
	require.Equal(t, largebody.NeedsRequestAssessment, disposition)
	require.Equal(t, largebody.StaticWireReasonNone, reason)
	require.NoError(t, largebody.DefaultTestWireTurnFacts().AssertNoShadowCall())
}
