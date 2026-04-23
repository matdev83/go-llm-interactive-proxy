package pluginreg_test

import (
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"gopkg.in/yaml.v3"
)

func TestValidateBundledFactories_succeedsForStandardRequirements(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBundleOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.ValidateBundledFactories(lipsdk.StandardDistributionRequirements()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateBundledFactories_explicitPartialRegistryFails(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBackendsOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.ValidateBundledFactories(lipsdk.StandardDistributionRequirements()); err == nil {
		t.Fatal("expected error when registry only has backends")
	}
}

func TestValidateBundledFactories_customRegistryIndependentOfDefaultCompleteness(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend(
		"validate-custom-only",
		func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			_ = n
			_ = upstream
			return execbackend.Backend{Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming)}, nil
		},
	); err != nil {
		t.Fatal(err)
	}

	req := []lipsdk.Requirement{{
		Kind: lipsdk.PluginKindBackend,
		ID:   "validate-custom-only",
	}}
	if err := reg.ValidateBundledFactories(req); err != nil {
		t.Fatal(err)
	}

	empty := pluginreg.NewRegistry()
	if err := empty.ValidateBundledFactories(req); err == nil {
		t.Fatal("expected empty registry to fail mandatory validation")
	}
}
