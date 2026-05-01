package standardbundle_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg/standardbundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestInstallOn_validatesStandardRequirements(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardbundle.InstallOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.ValidateBundledFactories(lipsdk.StandardDistributionRequirements()); err != nil {
		t.Fatal(err)
	}
}

func TestInstallOn_nilRegistry(t *testing.T) {
	t.Parallel()
	if err := standardbundle.InstallOn(nil, pluginreg.UpstreamAPIKeys{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestBundleExposesStandardTables(t *testing.T) {
	t.Parallel()
	b := standardbundle.Bundle()
	if len(b.Frontends) == 0 {
		t.Fatal("expected frontend registrations")
	}
	if len(b.Features) == 0 {
		t.Fatal("expected feature registrations")
	}
	if len(b.Backends) != 0 {
		t.Fatal("standardbundle.Bundle should not bind backend keys")
	}
}

func TestBackendBundleExposesBackends(t *testing.T) {
	t.Parallel()
	b := standardbundle.BackendBundle(pluginreg.UpstreamAPIKeys{})
	if len(b.Backends) == 0 {
		t.Fatal("expected backend registrations")
	}
}
