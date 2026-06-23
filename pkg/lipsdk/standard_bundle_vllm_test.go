package lipsdk_test

import (
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestStandardDistributionRequirements_includesVllmBackend(t *testing.T) {
	t.Parallel()
	req := lipsdk.StandardDistributionRequirements()
	found := slices.ContainsFunc(req, func(r lipsdk.Requirement) bool {
		return r.Kind == lipsdk.PluginKindBackend && r.ID == "vllm"
	})
	if !found {
		t.Fatal(`expected "vllm" backend in standard distribution requirements`)
	}
}
