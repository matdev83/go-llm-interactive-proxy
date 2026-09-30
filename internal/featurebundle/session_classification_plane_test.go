package featurebundle_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"
)

type mergeSessionClassifier struct{ id string }

func (c mergeSessionClassifier) ID() string { return c.id }

func (mergeSessionClassifier) Classify(context.Context, sessionclassification.Input) (session.Classification, error) {
	return session.Classification{}, nil
}

func TestGeneratedComposition_RejectsDuplicateSessionClassifiers(t *testing.T) {
	t.Parallel()

	makeBundle := func(pluginID string) feature.FeatureBundle {
		t.Helper()
		contributions := feature.NewContributionSet()
		classifier := mergeSessionClassifier{id: "classifier-" + pluginID}
		require.NoError(t, feature.Contribute(contributions, feature.PlaneSessionClassifier, pluginID, sessionclassification.Classifier(classifier)))
		return feature.BundleFromPlanes(contributions.Freeze(), nil)
	}

	merged, err := featurebundle.MergeBundlesGenerated(makeBundle("first"), makeBundle("second"))
	require.ErrorIs(t, err, feature.ErrExclusiveConflict)
	require.Equal(t, featurebundle.GeneratedMergeSurface{}, merged)
}
