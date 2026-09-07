package featurehost

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	featurecontinuity "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

// CompactionSchedulerBounds translates compaction continuity feature config into
// generic auxiliary scheduler bounds.
func CompactionSchedulerBounds(cfg *config.Config) auxreq.SchedulerConfig {
	return compaction.SchedulerBoundsFromConfig(cfg)
}

// validateCompactionPrerequisites gates only enabled official composition.
func (r *Runtime) validateCompactionPrerequisites(regs []lipsdk.Registration) error {
	hasDetector := r != nil && r.compactionDetector != nil
	hasCoordinator := r != nil && r.branchCoordinator != nil
	hasBackground := r != nil && r.bgAux != nil

	for _, reg := range regs {
		if reg.Kind != lipsdk.PluginKindFeature || !reg.Enabled || reg.RegistryFactoryKey() != featurecontinuity.ID {
			continue
		}
		cfg, err := featurecontinuity.DecodeConfig(reg.Config.Node)
		if err != nil {
			return fmt.Errorf("featurehost: compaction-continuity config: %w", err)
		}
		if err := featurecontinuity.ValidatePrerequisites(cfg, featurecontinuity.Prerequisites{
			DetectorPreview:   hasDetector,
			DetectorCommit:    hasDetector,
			BranchCoordinator: hasCoordinator,
			BackgroundAux:     hasBackground,
		}); err != nil {
			return fmt.Errorf("featurehost: generation prerequisite: %w", err)
		}
	}
	return nil
}

// bindCompactionContinuity replaces the configuration-only official preserver with
// the explicitly composed process parent port using generated typed replacement operations.
// The binder stages all registration replacements and commits only when the entire loop succeeds,
// ensuring fail-before-mutate transactional atomicity across all feature registrations.
func (r *Runtime) bindCompactionContinuity(genMerged featurebundle.GeneratedMergeSurface, regs []lipsdk.Registration) (featurebundle.GeneratedMergeSurface, error) {
	if r == nil || r.compactionParentPort == nil {
		return genMerged, nil
	}
	staged := genMerged
	for _, reg := range regs {
		if reg.Kind != lipsdk.PluginKindFeature || !reg.Enabled || reg.RegistryFactoryKey() != featurecontinuity.ID {
			continue
		}
		cfg, err := featurecontinuity.DecodeConfig(reg.Config.Node)
		if err != nil {
			return featurebundle.GeneratedMergeSurface{}, fmt.Errorf("featurehost: compaction-continuity config: %w", err)
		}
		bundle, err := featurecontinuity.FeatureBundleWithPort(cfg, r.compactionParentPort)
		if err != nil {
			return featurebundle.GeneratedMergeSurface{}, fmt.Errorf("featurehost: compaction-continuity composition: %w", err)
		}
		var bindErr error
		staged, bindErr = staged.BindCompactionPreservers(featurecontinuity.ID, lipfeature.Get(bundle.PlaneSet, lipfeature.PlaneCompactionPreservers))
		if bindErr != nil {
			return featurebundle.GeneratedMergeSurface{}, fmt.Errorf("featurehost: compaction-continuity binding: %w", bindErr)
		}
	}
	return staged, nil
}
