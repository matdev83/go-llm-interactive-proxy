package sessionclassification

import (
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

// FeatureBundle is the registry-facing bundle surface for session
// classification. The feature config has already been validated and decoded by
// the caller, and the concrete classifier needs the process-owned coordinator,
// so the exclusive classifier plane is published later as a generation-bound
// value during standard featurehost composition. A disabled or absent feature
// therefore contributes no plane and performs no state, schema, or network
// work at plugin build time.
func FeatureBundle(_ Config) lipfeature.FeatureBundle {
	return lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1}
}
