package standardplugins

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"gopkg.in/yaml.v3"
)

// TestStandardBackends_declareMaxOutputEnforcement pins the fail-closed
// authority-clamp contract: every standard backend must explicitly declare
// whether it serializes MaxOutputTokens on the wire. A new backend added to
// StandardBackendBundle without an entry in standardBackendEnforcesMaxOutput
// fails this test until classified, so an unknown adapter never silently
// accepts a spend-cap clamp it cannot bind.
func TestStandardBackends_declareMaxOutputEnforcement(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	keys := UpstreamAPIKeys{AlibabaTokenPlan: []string{"test"}}
	if err := InstallStandardBackendsOn(reg, keys); err != nil {
		t.Fatal(err)
	}

	for _, id := range standardBackendFactoryIDs(t) {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			want, ok := standardBackendEnforcesMaxOutput(id)
			if !ok {
				t.Fatalf("backend %q is not classified in standardBackendEnforcesMaxOutput; add an explicit entry", id)
			}
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(standardBackendEnforcementBuildYAML(id)), &node); err != nil {
				t.Fatal(err)
			}
			be, err := reg.BuildBackend(id, node, nil, pluginreg.BackendFactoryDeps{})
			if err != nil {
				t.Fatalf("BuildBackend(%q) error = %v", id, err)
			}
			if be.EnforcesMaxOutputTokens != want {
				t.Fatalf("backend %q EnforcesMaxOutputTokens = %v, want %v", id, be.EnforcesMaxOutputTokens, want)
			}
		})
	}
}

// standardBackendEnforcesMaxOutput returns the expected wire-enforcement
// declaration for each standard backend. True means the adapter serializes a
// non-nil/positive MaxOutputTokens so an authority spend-cap clamp binds;
// false means the protocol cannot represent it and the executor must exclude
// the candidate when a clamp is required. New backends must be added here.
func standardBackendEnforcesMaxOutput(id string) (bool, bool) {
	switch id {
	case "openai-responses", "openai-legacy", "anthropic", "alibaba-token-plan-intl", "gemini", "bedrock",
		CustomOpenAILegacyCompatibleID, CustomOpenAIResponsesCompatibleID, CustomAnthropicCompatibleID:
		return true, true
	default:
		return false, false
	}
}

// standardBackendEnforcementBuildYAML returns minimal YAML that builds the
// success-path backend (not a config-error backend) so the real
// EnforcesMaxOutputTokens declaration is observed. Credential-requiring
// backends get a dummy api_key appended to the shared base YAML.
func standardBackendEnforcementBuildYAML(id string) string {
	base := standardBackendBuildYAML(id)
	switch id {
	case "anthropic", "openai-legacy", "openai-responses",
		CustomOpenAILegacyCompatibleID, CustomOpenAIResponsesCompatibleID, CustomAnthropicCompatibleID:
		if !strings.Contains(base, "api_key:") {
			return base + "api_key: test\n"
		}
	}
	return base
}
