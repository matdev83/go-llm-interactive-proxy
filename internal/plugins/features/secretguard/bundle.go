package secretguard

import (
	"fmt"

	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

// FeatureBundle returns a schema-V1 feature bundle contributing one SecretGuard.
func FeatureBundle(cfg Config) lipfeature.FeatureBundle {
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneSecretGuards, ID, []sdk.Guard{NewGuard(cfg)}); err != nil {
		panic(fmt.Sprintf("%s: contribute secret guards: %v", ID, err))
	}
	return lipfeature.BundleFromPlanes(cs.Freeze(), nil)
}
