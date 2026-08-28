package compactioncompose

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	featurecontinuity "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// BindFeatureSurface replaces the configuration-only official preserver with
// the explicitly composed process parent port using generated typed replacement operations.
// The binder stages all registration replacements and commits only when the entire loop succeeds,
// ensuring fail-before-mutate transactional atomicity across all feature registrations.
func BindFeatureSurface(genMerged featurebundle.GeneratedMergeSurface, parent *CompactionContinuityParentPort, regs []lipsdk.Registration) (featurebundle.GeneratedMergeSurface, error) {
	staged := genMerged
	for _, reg := range regs {
		if reg.Kind != lipsdk.PluginKindFeature || !reg.Enabled || reg.RegistryFactoryKey() != featurecontinuity.ID {
			continue
		}
		cfg, err := featurecontinuity.DecodeConfig(reg.Config.Node)
		if err != nil {
			return featurebundle.GeneratedMergeSurface{}, fmt.Errorf("compactioncompose: compaction-continuity config: %w", err)
		}
		bundle, err := featurecontinuity.FeatureBundleWithPort(cfg, parent)
		if err != nil {
			return featurebundle.GeneratedMergeSurface{}, fmt.Errorf("compactioncompose: compaction-continuity composition: %w", err)
		}
		var bindErr error
		staged, bindErr = staged.BindCompactionPreservers(featurecontinuity.ID, bundle.CompactionPreservers)
		if bindErr != nil {
			return featurebundle.GeneratedMergeSurface{}, fmt.Errorf("compactioncompose: compaction-continuity binding: %w", bindErr)
		}
	}
	return staged, nil
}
