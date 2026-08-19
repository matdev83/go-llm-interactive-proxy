package runtimebundle

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// validateCompactionContinuityGeneration is the composition-only dependency
// gate. Feature factories intentionally receive opaque YAML and cannot inspect
// process-owned services; generation composition supplies the capability
// evidence here before candidate/model work begins.
func validateCompactionContinuityGeneration(ps *ProcessServices, regs []lipsdk.Registration) error {
	active := false
	for _, reg := range regs {
		if reg.Kind == lipsdk.PluginKindFeature && reg.Enabled && reg.RegistryFactoryKey() == compactioncontinuity.ID {
			active = true
			break
		}
	}
	if !active {
		return nil
	}
	if ps == nil {
		return fmt.Errorf("runtimebundle: compaction-continuity: nil ProcessServices")
	}
	for _, reg := range regs {
		if reg.Kind != lipsdk.PluginKindFeature || !reg.Enabled || reg.RegistryFactoryKey() != compactioncontinuity.ID {
			continue
		}
		cfg, err := compactioncontinuity.DecodeConfig(reg.Config.Node)
		if err != nil {
			return fmt.Errorf("runtimebundle: compaction-continuity config: %w", err)
		}
		if err := compactioncontinuity.ValidatePrerequisites(cfg, compactioncontinuity.Prerequisites{
			DetectorPreview:   ps.CompactionDetector != nil,
			DetectorCommit:    ps.CompactionDetector != nil,
			BranchCoordinator: ps.BranchCoordinator != nil,
			BackgroundAux:     ps.BackgroundAux != nil,
		}); err != nil {
			return fmt.Errorf("runtimebundle: generation prerequisite: %w", err)
		}
	}
	return nil
}
