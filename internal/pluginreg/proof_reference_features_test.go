package pluginreg_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refautoappend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftoolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftraffictranscript"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refverifier"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refworkspaceguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
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
	if err := pluginreg.InstallStandardBundleOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
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
	if err := pluginreg.InstallStandardBundleOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
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
	m, err := reg.MergeFeatureSurface(regs)
	if err != nil {
		t.Fatal(err)
	}
	const need = 1
	// Coarse shape: each proof contributes at least one non-hook surface.
	if len(m.SessionOpeners) < need {
		t.Fatalf("openers: %d", len(m.SessionOpeners))
	}
	if len(m.RequestTransforms) < need {
		t.Fatalf("request transforms: %d", len(m.RequestTransforms))
	}
	if len(m.ToolCatalogFilters) < need {
		t.Fatalf("catalog: %d", len(m.ToolCatalogFilters))
	}
	if len(m.Hooks.ToolReactors) < need {
		t.Fatalf("reactors: %d", len(m.Hooks.ToolReactors))
	}
	if len(m.WorkspaceResolvers) < need {
		t.Fatalf("workspace: %d", len(m.WorkspaceResolvers))
	}
	if len(m.TrafficObservers) < need {
		t.Fatalf("obs: %d", len(m.TrafficObservers))
	}
	if len(m.UsageObservers) < need {
		t.Fatalf("usage observers: %d", len(m.UsageObservers))
	}
	if len(m.RawCaptureSinks) < need {
		t.Fatalf("raw: %d", len(m.RawCaptureSinks))
	}
	if len(m.TrafficRedactors) < need {
		t.Fatalf("red: %d", len(m.TrafficRedactors))
	}
	if len(m.CompletionGates) < need {
		t.Fatalf("gates: %d", len(m.CompletionGates))
	}
}
