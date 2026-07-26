package lipsdk_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestStandardDistributionRequirements_excludesOllamaBackends(t *testing.T) {
	t.Parallel()
	for _, r := range lipsdk.StandardDistributionRequirements() {
		if r.Kind == lipsdk.PluginKindBackend && (r.ID == "ollama" || r.ID == "ollama-cloud") {
			t.Fatalf("%s must not be mandatory after Phase 7 externalization", r.ID)
		}
	}
}
