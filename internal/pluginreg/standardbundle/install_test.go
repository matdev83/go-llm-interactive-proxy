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
	if err := standardbundle.InstallOn(reg); err != nil {
		t.Fatal(err)
	}
	if err := reg.ValidateBundledFactories(lipsdk.StandardDistributionRequirements()); err != nil {
		t.Fatal(err)
	}
}

func TestInstallOn_nilRegistry(t *testing.T) {
	t.Parallel()
	if err := standardbundle.InstallOn(nil); err == nil {
		t.Fatal("expected error")
	}
}
