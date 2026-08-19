package compactioncompose

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	featurecontinuity "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
)

// BindFeatureSurface replaces the configuration-only official preserver with
// the explicitly composed process parent port before extension snapshots.
func BindFeatureSurface(merged featurebundle.MergedFeatureSurface, parent *CompactionContinuityParentPort, regs []lipsdk.Registration) (featurebundle.MergedFeatureSurface, error) {
	for _, reg := range regs {
		if reg.Kind != lipsdk.PluginKindFeature || !reg.Enabled || reg.RegistryFactoryKey() != featurecontinuity.ID {
			continue
		}
		cfg, err := featurecontinuity.DecodeConfig(reg.Config.Node)
		if err != nil {
			return featurebundle.MergedFeatureSurface{}, fmt.Errorf("compactioncompose: compaction-continuity config: %w", err)
		}
		bundle, err := featurecontinuity.FeatureBundleWithPort(cfg, parent)
		if err != nil {
			return featurebundle.MergedFeatureSurface{}, fmt.Errorf("compactioncompose: compaction-continuity composition: %w", err)
		}
		preservers := make([]compaction.Preserver, 0, len(merged.CompactionPreservers)+len(bundle.CompactionPreservers))
		for _, preserver := range merged.CompactionPreservers {
			if safePreserverID(preserver) == featurecontinuity.ID {
				continue
			}
			preservers = append(preservers, preserver)
		}
		preservers = append(preservers, bundle.CompactionPreservers...)
		merged.CompactionPreservers = preservers
	}
	return merged, nil
}

func safePreserverID(p compaction.Preserver) (id string) {
	defer func() {
		if recover() != nil {
			id = ""
		}
	}()
	if p != nil {
		return p.ID()
	}
	return ""
}
