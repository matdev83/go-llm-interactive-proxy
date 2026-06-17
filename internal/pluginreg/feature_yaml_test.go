package pluginreg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/submitnoop"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"gopkg.in/yaml.v3"
)

func testRegistryWithStdBundle(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	if err := InstallStandardBundleOn(r, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestStandardBundle_buildsPreRequestPolicyHandlers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.md"), []byte("classify the request"), 0o600); err != nil {
		t.Fatal(err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal(fmt.Appendf(nil, `
prompt_dir: %q
handlers:
  - id: compliance
    priority: 3
    prompt_filename: policy.md
    model_routing_string: local:policy
    deny_pattern: DENY
`, dir), &n); err != nil {
		t.Fatal(err)
	}
	reg := testRegistryWithStdBundle(t)
	b, err := reg.BuildFeatureBundle("pre-request-policy", n)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.PreRequestHandlers) != 1 {
		t.Fatalf("pre-request handlers: %d", len(b.PreRequestHandlers))
	}
	if b.PreRequestHandlers[0].ID() != "compliance" {
		t.Fatalf("handler id: %q", b.PreRequestHandlers[0].ID())
	}
}

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
	reg := testRegistryWithStdBundle(t)
	_, _, err := reg.BuildFeatureHooks([]lipsdk.Registration{
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
	reg := testRegistryWithStdBundle(t)
	_, _, err := reg.BuildFeatureHooks([]lipsdk.Registration{
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
	reg := testRegistryWithStdBundle(t)
	hookCfg, lifes, err := reg.BuildFeatureHooks([]lipsdk.Registration{
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
	reg := testRegistryWithStdBundle(t)
	hookCfg, lifes, err := reg.BuildFeatureHooks([]lipsdk.Registration{
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
