package qa

import (
	"strings"
	"testing"
)

func TestOpenResponsesOfficialCompliance_UsesScopeAwareSuccessfulBypass(t *testing.T) {
	t.Parallel()
	workflow := readRepositoryFile(t, ".github", "workflows", "openresponses-official-compliance.yml")

	for _, needle := range []string{
		"name: Pinned official 17-case suite",
		"id: scope",
		"id: manifest",
		"id: static_archtest",
		"id: suite",
		"if: steps.scope.outputs.run_suite == 'true'",
		"if: always() && steps.scope.outputs.run_suite == 'true'",
		"Report unrelated PR bypass",
		"OpenResponses official compliance suite bypassed: no relevant files changed.",
		"The official-suite job remains successful for branch-protection semantics.",
		"Fail closed if scope detection failed",
		"steps.scope.outcome == 'success'",
	} {
		if !strings.Contains(workflow, needle) {
			t.Fatalf("official compliance workflow missing scope/bypass contract %q", needle)
		}
	}

	// The executable matcher self-test runs in the Ubuntu workflow. This Go
	// contract remains portable because Bash is not guaranteed on Windows.
	scopeScript := readRepositoryFile(t, "scripts", "openresponses-compliance-scope.sh")
	for _, path := range []string{
		"internal/integration/openresponses/**",
		"internal/archtest/**",
		"internal/plugins/frontends/openresponses/**",
		"internal/plugins/frontends/openairesponses/**",
		"internal/plugins/backends/openresponsescompat/**",
		"internal/plugins/backends/openairesponses/**",
		"internal/plugins/protocols/openresponses/**",
		"internal/plugins/protocols/openairesponsesitem/**",
		"internal/refclient/openairesponses/**",
		"internal/refbackend/openairesponses/**",
		"internal/testkit/conformance/**",
		"internal/core/**",
		"internal/stdhttp/**",
		"pkg/lipapi/**",
		"tools/openresponses-compliance/**",
		"Makefile",
		"go.mod",
		"go.sum",
	} {
		if !strings.Contains(scopeScript, path) {
			t.Errorf("scope matcher missing relevant path %q", path)
		}
	}

	for _, needle := range []string{
		"internal/plugins/frontends/openairesponses/**",
		"internal/plugins/protocols/openairesponsesitem/**",
		"internal/testkit/conformance/**",
		"file_requires_suite",
		"--self-test",
		"printf 'true\\n'",
		"printf 'false\\n'",
	} {
		if !strings.Contains(scopeScript, needle) {
			t.Errorf("scope matcher missing regression contract %q", needle)
		}
	}

	if strings.Contains(workflow, "\n    paths:") {
		t.Fatal("workflow-level paths would skip the required job instead of reporting a successful bypass")
	}
	if strings.Contains(scopeScript, "docs/**") || strings.Contains(scopeScript, "internal/core/routing/**") {
		t.Fatal("scope matcher must not broaden to unrelated documentation or routing paths")
	}
}
