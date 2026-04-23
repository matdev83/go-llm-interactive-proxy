package pluginreg

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/submitnoop"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"gopkg.in/yaml.v3"
)

func TestBuildFeatureHooks_partialBundlesLeaveOtherChainsAbsent(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	submitFacID := "test-fac-submit-" + strings.ReplaceAll(t.Name(), "/", "-")
	toolFacID := "test-fac-tool-" + strings.ReplaceAll(t.Name(), "/", "-")

	if err := reg.RegisterFeature(submitFacID, FeatureFactoryFromHooks(func(n yaml.Node) (hooks.Config, []lipplugin.Lifecycle, error) {
		cfg, err := submitnoop.DecodeHookConfig(n)
		if err != nil {
			return hooks.Config{}, nil, err
		}
		return hooks.Config{SubmitHooks: []sdk.SubmitHook{submitnoop.NewSubmitHookWithConfig(cfg)}}, nil, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterFeature(toolFacID, FeatureFactoryFromHooks(featureToolReactorNoop)); err != nil {
		t.Fatal(err)
	}

	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	hookCfg, _, err := reg.BuildFeatureHooks([]lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "inst-submit", FactoryKind: submitFacID, Enabled: true, Config: lipsdk.ConfigPayload{Node: cfgNode}},
		{Kind: lipsdk.PluginKindFeature, ID: "inst-tool", FactoryKind: toolFacID, Enabled: true, Config: lipsdk.ConfigPayload{Node: cfgNode}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hookCfg.SubmitHooks) != 1 {
		t.Fatalf("submit hooks: %d", len(hookCfg.SubmitHooks))
	}
	if len(hookCfg.ToolReactors) != 1 {
		t.Fatalf("tool reactors: %d", len(hookCfg.ToolReactors))
	}
	if len(hookCfg.RequestPartHooks) != 0 {
		t.Fatalf("expected no request part hooks, got %d", len(hookCfg.RequestPartHooks))
	}
	if len(hookCfg.ResponsePartHooks) != 0 {
		t.Fatalf("expected no response part hooks, got %d", len(hookCfg.ResponsePartHooks))
	}

	bus := hooks.New(hookCfg)
	ns, nrq, nrs, nt := bus.HookChainLengths()
	if ns != 1 || nrq != 0 || nrs != 0 || nt != 1 {
		t.Fatalf("hook chain lengths: submit=%d request=%d response=%d tools=%d", ns, nrq, nrs, nt)
	}
}
