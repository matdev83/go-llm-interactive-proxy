package testkit

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

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
