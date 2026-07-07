package pluginreg_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"gopkg.in/yaml.v3"
)

func TestBuildFeatureHooks_usesExplicitRegistryNotDefault(t *testing.T) {
	t.Parallel()

	factoryID := "custom-registry-feature-" + strings.ReplaceAll(t.Name(), "/", "-")
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterFeature(
		factoryID,
		func(n yaml.Node) (lipfeature.FeatureBundle, error) {
			_ = n
			return lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1}, nil
		},
	); err != nil {
		t.Fatal(err)
	}

	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	regs := []lipsdk.Registration{{
		Kind:        lipsdk.PluginKindFeature,
		ID:          "feat-inst",
		FactoryKind: factoryID,
		Enabled:     true,
		Config:      lipsdk.ConfigPayload{Node: cfgNode},
	}}

	if _, _, err := runtimebundle.BuildFeatureHooks(reg, regs); err != nil {
		t.Fatal(err)
	}
	empty := pluginreg.NewRegistry()
	if _, _, err := runtimebundle.BuildFeatureHooks(empty, regs); err == nil {
		t.Fatal("expected empty registry to miss custom-only feature factory")
	}
}
