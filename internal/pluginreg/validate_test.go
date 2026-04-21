package pluginreg_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestValidateBundledFactories_succeedsForStandardRequirements(t *testing.T) {
	t.Parallel()
	if err := pluginreg.ValidateBundledFactories(lipsdk.StandardDistributionRequirements()); err != nil {
		t.Fatal(err)
	}
}
