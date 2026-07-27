package lipsdk_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestStandardDistributionRequirements_excludesOpenCodeBackends(t *testing.T) {
	t.Parallel()
	for _, r := range lipsdk.StandardDistributionRequirements() {
		if r.Kind == lipsdk.PluginKindBackend && (r.ID == "opencode-go" || r.ID == "opencode-zen") {
			t.Fatalf("%s must not be mandatory after Phase 8.1 externalization", r.ID)
		}
	}
}
