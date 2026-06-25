package pluginreg

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/localstub"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"gopkg.in/yaml.v3"
)

func TestStandardBackends_includeLocalStubFactory(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`text: "x"`), &n); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.BuildBackend(localstub.ID, n, nil, BackendFactoryDeps{}); err != nil {
		t.Fatal(err)
	}
	p, ok := reg.BackendSecurityProfile(localstub.ID)
	if !ok || p.CredentialMode != CredentialNone {
		t.Fatalf("profile: ok=%v mode=%q", ok, p.CredentialMode)
	}
}

func TestStandardBundle_validateMandatoryWithLocalStub(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.ValidateBundledFactories(lipsdk.StandardDistributionRequirements()); err != nil {
		t.Fatal(err)
	}
}
