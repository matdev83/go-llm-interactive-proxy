package pluginreg

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/submitnoop"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"gopkg.in/yaml.v3"
)

func TestRequireEmptyFeatureYAML_acceptsEmptyMapping(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &n); err != nil {
		t.Fatal(err)
	}
	if err := requireEmptyFeatureYAML("parts-noop", n); err != nil {
		t.Fatal(err)
	}
}

func TestRequireEmptyFeatureYAML_rejectsUnknownKey(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("unexpected: true"), &n); err != nil {
		t.Fatal(err)
	}
	if err := requireEmptyFeatureYAML("parts-noop", n); err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildFeatureHooks_rejectsUnknownNoopConfig(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("foo: bar"), &n); err != nil {
		t.Fatal(err)
	}
	_, _, err := BuildFeatureHooks([]lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "submit-noop", Enabled: true, Config: lipsdk.ConfigPayload{Node: n}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildFeatureHooks_acceptsEmptyConfig(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &n); err != nil {
		t.Fatal(err)
	}
	_, _, err := BuildFeatureHooks([]lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "submit-noop", Enabled: true, Config: lipsdk.ConfigPayload{Node: n}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBuildFeatureHooks_submitNoopLifecycleProbe(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("lifecycle_probe: true"), &n); err != nil {
		t.Fatal(err)
	}
	hookCfg, lifes, err := BuildFeatureHooks([]lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "submit-noop", Enabled: true, Config: lipsdk.ConfigPayload{Node: n}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hookCfg.SubmitHooks) != 1 {
		t.Fatalf("submit hooks: %d", len(hookCfg.SubmitHooks))
	}
	if len(lifes) != 1 {
		t.Fatalf("lifecycles: %d", len(lifes))
	}
	probe, ok := lifes[0].(*submitnoop.LifecycleProbe)
	if !ok {
		t.Fatalf("wrong lifecycle type %T", lifes[0])
	}
	ctx := context.Background()
	if err := probe.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !probe.WasStarted() {
		t.Fatal("expected Start to run")
	}
	if err := probe.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if !probe.WasStopped() {
		t.Fatal("expected Stop to run")
	}
}

func TestBuildFeatureHooks_submitNoopOrderFromConfig(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("order: 3"), &n); err != nil {
		t.Fatal(err)
	}
	hookCfg, lifes, err := BuildFeatureHooks([]lipsdk.Registration{
		{Kind: lipsdk.PluginKindFeature, ID: "submit-noop", Enabled: true, Config: lipsdk.ConfigPayload{Node: n}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lifes) != 0 {
		t.Fatalf("unexpected lifecycles: %d", len(lifes))
	}
	if hookCfg.SubmitHooks[0].Order() != 3 {
		t.Fatalf("order: %d", hookCfg.SubmitHooks[0].Order())
	}
}
