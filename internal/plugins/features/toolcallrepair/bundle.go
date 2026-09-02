package toolcallrepair

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolcallrepair/repair"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

// FeatureBundle constructs a schema-V1 feature bundle from validated configuration,
// contributing the tool-call repair finalizer and finalization max args byte limit.
func FeatureBundle(cfg Config) (lipfeature.FeatureBundle, error) {
	norm, err := normalizeConfig(cfg, cfg.MaxArgsBytes != 0)
	if err != nil {
		return lipfeature.FeatureBundle{}, err
	}
	fin := repair.NewFinalizer(repair.FinalizerPolicy{
		ID:             ID,
		MaxArgsBytes:   norm.MaxArgsBytes,
		OnUnrepairable: norm.OnUnrepairable,
		Order:          norm.FinalizerOrder(),
		Schema: repair.SchemaLimits{
			MaxSchemaBytes:   norm.Schema.MaxSchemaBytes,
			MaxNestingDepth:  norm.Schema.MaxNestingDepth,
			MaxNodes:         norm.Schema.MaxNodes,
			MaxProperties:    norm.Schema.MaxProperties,
			MaxLocalRefDepth: norm.Schema.MaxLocalRefDepth,
			MaxCacheEntries:  norm.Schema.MaxCacheEntries,
			MaxCacheBytes:    norm.Schema.MaxCacheBytes,
		},
	})
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizers, ID, []toolcall.Finalizer{fin}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", ID, err)
	}
	if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, ID, norm.MaxArgsBytes); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", ID, err)
	}
	b := lipfeature.BundleFromPlanes(cs.Freeze(), nil)
	if err := b.Validate(); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", ID, err)
	}
	return b, nil
}
