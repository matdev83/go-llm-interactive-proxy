package standardplugins_test

import (
	"testing"

	corerepair "github.com/matdev83/go-llm-interactive-proxy/internal/core/toolcallrepair"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolcallrepair"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"gopkg.in/yaml.v3"
)

func TestToolCallRepairFactory_RegistersFinalizer(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &n); err != nil {
		t.Fatal(err)
	}
	bundle, err := reg.BuildFeatureBundle(standardplugins.ToolCallRepairFeatureID, n)
	if err != nil {
		t.Fatalf("BuildFeatureBundle: %v", err)
	}
	finalizers := lipfeature.Get(bundle.PlaneSet, lipfeature.PlaneToolCallFinalizers)
	if len(finalizers) != 1 {
		t.Fatalf("want 1 finalizer, got %d", len(finalizers))
	}
	if finalizers[0].ID() != standardplugins.ToolCallRepairFeatureID {
		t.Fatalf("id=%q", finalizers[0].ID())
	}
	maxArgs := lipfeature.Get(bundle.PlaneSet, lipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	if maxArgs <= 0 {
		t.Fatalf("factory must contribute max_args_bytes, got %d", maxArgs)
	}
	if err := reg.ValidateBundledFactories([]lipsdk.Requirement{
		{Kind: lipsdk.PluginKindFeature, ID: standardplugins.ToolCallRepairFeatureID},
	}); err != nil {
		t.Fatalf("factory missing from registry: %v", err)
	}
}

// TestToolCallRepairFactory_DefaultMaxArgsBytesMatchCore locks composition
// wiring: empty YAML yields core/feature DefaultMaxArgsBytes on the bundle.
func TestToolCallRepairFactory_DefaultMaxArgsBytesMatchCore(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &n); err != nil {
		t.Fatal(err)
	}
	bundle, err := reg.BuildFeatureBundle(standardplugins.ToolCallRepairFeatureID, n)
	if err != nil {
		t.Fatalf("BuildFeatureBundle: %v", err)
	}
	maxArgs := lipfeature.Get(bundle.PlaneSet, lipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	if maxArgs != corerepair.DefaultMaxArgsBytes {
		t.Fatalf("bundle max_args=%d != core DefaultMaxArgsBytes=%d",
			maxArgs, corerepair.DefaultMaxArgsBytes)
	}
	if maxArgs != toolcallrepair.DefaultMaxArgsBytes {
		t.Fatalf("bundle max_args=%d != feature DefaultMaxArgsBytes=%d",
			maxArgs, toolcallrepair.DefaultMaxArgsBytes)
	}
}
