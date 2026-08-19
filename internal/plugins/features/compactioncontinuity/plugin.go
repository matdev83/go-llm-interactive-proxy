package compactioncontinuity

import lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"

// FeatureBundle returns the schema-valid composition contribution for the
// configuration phase. Semantic preservation callbacks are intentionally added
// by the later capsule/extractor tasks; this task must not fabricate services
// or submit model work.
func FeatureBundle(_ Config) lipfeature.FeatureBundle {
	return lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1}
}
