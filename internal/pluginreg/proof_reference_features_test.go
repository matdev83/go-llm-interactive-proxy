package pluginreg_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refautoappend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftoolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftraffictranscript"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refverifier"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refworkspaceguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"gopkg.in/yaml.v3"
)

// proofReferenceIDs are the stage-four reference proof plugins (design §19, task 11).
var proofReferenceIDs = []string{
	refautoappend.ID,
	reftoolpolicy.ID,
	refworkspaceguard.ID,
	reftraffictranscript.ID,
	refverifier.ID,
}

func TestProofReferenceFeatures_buildEmptyYAML(t *testing.T) {
	t.Parallel()
	reg := &pluginreg.Registry{}
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	for _, id := range proofReferenceIDs {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			b, err := reg.BuildFeatureBundle(id, empty)
			if err != nil {
				t.Fatalf("%s: %v", id, err)
			}
			if b.SchemaVersion == 0 {
				t.Fatalf("%s: missing schema version", id)
			}
		})
	}
}

func TestProofReferenceFeatures_mergeSurface(t *testing.T) {
	t.Parallel()
	reg := &pluginreg.Registry{}
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var regs []lipsdk.Registration
	var empty yaml.Node
	for _, id := range proofReferenceIDs {
		regs = append(regs, lipsdk.Registration{
			Kind:        lipsdk.PluginKindFeature,
			ID:          id,
			FactoryKind: id,
			Enabled:     true,
			Config:      lipsdk.ConfigPayload{Node: empty},
		})
	}
	gen, err := featurebundle.MergeFeatureSurfaceGenerated(reg, regs)
	if err != nil {
		t.Fatal(err)
	}
	const need = 1
	// Coarse shape: each proof contributes at least one non-hook surface.
	if len(lipfeature.Get(gen.Frozen, lipfeature.PlaneSessionOpeners)) < need {
		t.Fatalf("openers: %d", len(lipfeature.Get(gen.Frozen, lipfeature.PlaneSessionOpeners)))
	}
	if len(lipfeature.Get(gen.Frozen, lipfeature.PlaneRequestTransforms)) < need {
		t.Fatalf("request transforms: %d", len(lipfeature.Get(gen.Frozen, lipfeature.PlaneRequestTransforms)))
	}
	if len(lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCatalogFilters)) < need {
		t.Fatalf("catalog: %d", len(lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCatalogFilters)))
	}
	if len(lipfeature.Get(gen.Frozen, lipfeature.PlaneWorkspaceResolvers)) < need {
		t.Fatalf("workspace: %d", len(lipfeature.Get(gen.Frozen, lipfeature.PlaneWorkspaceResolvers)))
	}
	if len(lipfeature.Get(gen.Frozen, lipfeature.PlaneTrafficObservers)) < need {
		t.Fatalf("obs: %d", len(lipfeature.Get(gen.Frozen, lipfeature.PlaneTrafficObservers)))
	}
	if len(lipfeature.Get(gen.Frozen, lipfeature.PlaneUsageObservers)) < need {
		t.Fatalf("usage observers: %d", len(lipfeature.Get(gen.Frozen, lipfeature.PlaneUsageObservers)))
	}
	if len(lipfeature.Get(gen.Frozen, lipfeature.PlaneRawCaptureSinks)) < need {
		t.Fatalf("raw: %d", len(lipfeature.Get(gen.Frozen, lipfeature.PlaneRawCaptureSinks)))
	}
	if len(lipfeature.Get(gen.Frozen, lipfeature.PlaneTrafficRedactors)) < need {
		t.Fatalf("red: %d", len(lipfeature.Get(gen.Frozen, lipfeature.PlaneTrafficRedactors)))
	}
	if len(lipfeature.Get(gen.Frozen, lipfeature.PlaneCompletionGates)) < need {
		t.Fatalf("gates: %d", len(lipfeature.Get(gen.Frozen, lipfeature.PlaneCompletionGates)))
	}
}
