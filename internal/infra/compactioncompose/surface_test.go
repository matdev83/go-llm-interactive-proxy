package compactioncompose

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	featurecontinuity "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"gopkg.in/yaml.v3"
)

func TestBindFeatureSurface_zeroObserversComposesOfficialPreserver(t *testing.T) {
	t.Parallel()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("extractor:\n  enabled: true\n  route: inherit\n"), &node); err != nil {
		t.Fatal(err)
	}
	res, err := BindFeatureSurface(featurebundle.GeneratedMergeSurface{}, nil, []lipsdk.Registration{{ID: featurecontinuity.ID, FactoryKind: featurecontinuity.ID, Kind: lipsdk.PluginKindFeature, Enabled: true, Config: lipsdk.ConfigPayload{Node: node}}})
	if err != nil {
		t.Fatal(err)
	}
	obs := lipfeature.Get(res.Frozen, lipfeature.PlaneCompactionObservers)
	pres := lipfeature.Get(res.Frozen, lipfeature.PlaneCompactionPreservers)
	if len(obs) != 0 || len(pres) != 1 || pres[0].ID() != featurecontinuity.ID {
		t.Fatalf("observers=%d preservers=%v", len(obs), pres)
	}
}
