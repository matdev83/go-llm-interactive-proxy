package compactioncompose

import (
	"fmt"

	featurecontinuity "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// ValidateFeaturePrerequisites gates only enabled official composition.
func ValidateFeaturePrerequisites(regs []lipsdk.Registration, detector, coordinator, background bool) error {
	for _, reg := range regs {
		if reg.Kind != lipsdk.PluginKindFeature || !reg.Enabled || reg.RegistryFactoryKey() != featurecontinuity.ID {
			continue
		}
		cfg, err := featurecontinuity.DecodeConfig(reg.Config.Node)
		if err != nil {
			return fmt.Errorf("compactioncompose: compaction-continuity config: %w", err)
		}
		if err := featurecontinuity.ValidatePrerequisites(cfg, featurecontinuity.Prerequisites{DetectorPreview: detector, DetectorCommit: detector, BranchCoordinator: coordinator, BackgroundAux: background}); err != nil {
			return fmt.Errorf("compactioncompose: generation prerequisite: %w", err)
		}
	}
	return nil
}
