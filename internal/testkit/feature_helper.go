package testkit

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

// FeatureBundle constructs a FeatureBundle by contributing into a new ContributionSet,
// freezing it, and wrapping it via lipfeature.BundleFromPlanes.
// If contribute is nil, an empty PlaneSet is used.
// If contribute fails, the test fails via t.Fatalf (or panics if t is nil).
func FeatureBundle(
	t testing.TB,
	contributorID string,
	contribute func(*lipfeature.ContributionSet) error,
	lifecycles []lipplugin.Lifecycle,
) lipfeature.FeatureBundle {
	if t != nil {
		t.Helper()
	}
	if contributorID == "" {
		contributorID = "test-feature"
	}
	cs := lipfeature.NewContributionSet()
	if contribute != nil {
		if err := contribute(cs); err != nil {
			if t != nil {
				t.Fatalf("testkit.FeatureBundle: contribute: %v", err)
			}
			panic(err)
		}
	}
	frozen := cs.Freeze()
	return lipfeature.BundleFromPlanes(frozen, lifecycles)
}

// FreezeBundle merges one or more FeatureBundles through generated plane adapters
// and returns the resulting FrozenPlaneSet. If any bundle has SchemaVersion unset,
// it is populated with SchemaVersionV1. Panics if merging fails, making it ideal
// for concise test setup.
func FreezeBundle(bundles ...lipfeature.FeatureBundle) lipfeature.FrozenPlaneSet {
	for i := range bundles {
		if bundles[i].SchemaVersion == 0 {
			bundles[i].SchemaVersion = lipfeature.SchemaVersionV1
		}
	}
	gen, err := featurebundle.MergeBundlesGenerated(bundles...)
	if err != nil {
		panic(err)
	}
	return gen.Frozen
}
