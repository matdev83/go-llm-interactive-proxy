package secretguard

import (
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

// FeatureBundle returns a schema-V1 feature bundle contributing one SecretGuard.
func FeatureBundle(cfg Config) lipfeature.FeatureBundle {
	return lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		SecretGuards:  []sdk.Guard{NewGuard(cfg)},
	}
}
