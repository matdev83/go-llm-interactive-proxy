package compactioncompose

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	featurecontinuity "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"gopkg.in/yaml.v3"
)

func TestBindFeatureSurface_zeroObserversComposesOfficialPreserver(t *testing.T) {
	t.Parallel()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("extractor:\n  enabled: true\n  route: inherit\n"), &node); err != nil {
		t.Fatal(err)
	}
	merged, err := BindFeatureSurface(featurebundle.MergedFeatureSurface{}, nil, []lipsdk.Registration{{ID: featurecontinuity.ID, FactoryKind: featurecontinuity.ID, Kind: lipsdk.PluginKindFeature, Enabled: true, Config: lipsdk.ConfigPayload{Node: node}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.CompactionObservers) != 0 || len(merged.CompactionPreservers) != 1 || merged.CompactionPreservers[0].ID() != featurecontinuity.ID {
		t.Fatalf("observers=%d preservers=%v", len(merged.CompactionObservers), merged.CompactionPreservers)
	}
}
