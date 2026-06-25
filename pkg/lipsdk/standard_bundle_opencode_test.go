package lipsdk_test

import (
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestStandardDistributionRequirements_includesOpenCodeBackends(t *testing.T) {
	t.Parallel()
	req := lipsdk.StandardDistributionRequirements()
	for _, id := range []string{"opencode-go", "opencode-zen"} {
		found := slices.ContainsFunc(req, func(r lipsdk.Requirement) bool {
			return r.Kind == lipsdk.PluginKindBackend && r.ID == id
		})
		if !found {
			t.Fatalf("expected %q backend in standard distribution requirements", id)
		}
	}
}
