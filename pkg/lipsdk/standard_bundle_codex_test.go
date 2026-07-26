package lipsdk_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestStandardDistributionRequirements_excludesCodexBackends(t *testing.T) {
	t.Parallel()
	for _, r := range lipsdk.StandardDistributionRequirements() {
		if r.Kind == lipsdk.PluginKindBackend && (r.ID == "openai-codex" || r.ID == "openai-codex-app-server") {
			t.Fatalf("%s must not be mandatory after Phase 8.2 externalization", r.ID)
		}
	}
}
