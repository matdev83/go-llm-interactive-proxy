package standardbundle_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg/standardbundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestInstall_matchesRegisterStandardBundle(t *testing.T) {
	t.Parallel()
	// Standard bundle entrypoint used by cmd is RegisterStandardBundle; standardbundle.Install delegates.
	standardbundle.Install()
	if err := pluginreg.ValidateBundledFactories(lipsdk.StandardDistributionRequirements()); err != nil {
		t.Fatal(err)
	}
}
