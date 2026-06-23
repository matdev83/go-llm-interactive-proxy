package lipsdk_test

import (
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestStandardDistributionRequirements_includesLmstudioBackend(t *testing.T) {
	t.Parallel()
	req := lipsdk.StandardDistributionRequirements()
	found := slices.ContainsFunc(req, func(r lipsdk.Requirement) bool {
		return r.Kind == lipsdk.PluginKindBackend && r.ID == "lmstudio"
	})
	if !found {
		t.Fatal(`expected "lmstudio" backend in standard distribution requirements`)
	}
}
