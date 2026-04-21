package pluginreg_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"gopkg.in/yaml.v3"
)

func TestBuildFeatureHooks_usesExplicitRegistryNotDefault(t *testing.T) {
	t.Parallel()

	factoryID := "custom-registry-feature-" + strings.ReplaceAll(t.Name(), "/", "-")
	reg := pluginreg.NewRegistry()
	reg.RegisterFeature(factoryID, func(n yaml.Node) (hooks.Config, []lipplugin.Lifecycle, error) {
		_ = n
		return hooks.Config{}, nil, nil
	})

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

	if _, _, err := reg.BuildFeatureHooks(regs); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pluginreg.BuildFeatureHooks(regs); err == nil {
		t.Fatal("expected default registry to miss custom-only feature factory")
	}
}
