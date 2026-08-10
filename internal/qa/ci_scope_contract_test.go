package qa

import (
	"os"
	"strings"
	"testing"
)

func TestCIScopeClassifier_Contracts(t *testing.T) {
	t.Parallel()
	script := readRepositoryFile(t, "scripts", "ci-scope.sh")
	for _, needle := range []string{
		"--outputs BASE_SHA HEAD_SHA",
		"git diff --name-only -z",
		"openresponses_coverage",
		"scripts/ci-scope.sh|scripts/openresponses-compliance-scope.sh",
		"OK: CI scope self-test",
		"set -euo pipefail",
		"scripts/openresponses-compliance-scope.sh",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("CI scope classifier missing contract %q", needle)
		}
	}

	workflowExpectations := map[string][]string{
		"ci.yml": {
			"test: ${{ steps.filter.outputs.test }}",
			"bash scripts/ci-scope.sh --self-test",
			"CI test/build matrix bypassed: no test-relevant changes detected.",
			"if: always()",
			"Fail closed if scope detection failed",
			"CI test/build matrix bypassed: no test-relevant changes detected.",
		},
		"qa.yml": {
			"test: ${{ steps.filter.outputs.test }}",
			"bash scripts/ci-scope.sh --self-test",
			"if: always()",
			"Fail closed if scope detection failed",
			"QA bypassed: no test-relevant changes detected.",
		},
		"codeql.yml": {
			"go: ${{ steps.filter.outputs.go }}",
			"bash scripts/ci-scope.sh --self-test",
			"if: always()",
			"Fail closed if scope detection failed",
			"Go CodeQL bypassed: no Go-relevant changes detected.",
		},
		"openresponses-coverage.yml": {
			"name: Detect OpenResponses coverage scope",
			"bash scripts/ci-scope.sh --self-test",
			"run_suite: ${{ steps.filter.outputs.run_suite }}",
			"if: always()",
			"Fail closed if scope detection failed",
			"OpenResponses coverage bypassed: no measured-surface changes detected.",
		},
	}
	for workflow, needles := range workflowExpectations {
		contents := readRepositoryFile(t, ".github", "workflows", workflow)
		for _, needle := range needles {
			if !strings.Contains(contents, needle) {
				t.Errorf("%s missing scope/bypass contract %q", workflow, needle)
			}
		}
		if strings.Contains(contents, "\n    paths:") {
			t.Errorf("%s must keep the workflow present so bypass reports success", workflow)
		}
	}
	if !strings.Contains(script, "scripts/ci-scope.sh|scripts/openresponses-compliance-scope.sh") {
		t.Fatal("scope classifier must keep the two self-tested scope scripts outside heavy Go test/CodeQL scope")
	}
}

func TestCIScopeClassifier_SelfTest(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat(repositoryFile(t, "scripts", "ci-scope.sh")); err != nil {
		t.Fatal(err)
	}
	// The executable Bash self-test is run by the workflow contract; this test
	// keeps the repository path contract portable for Windows unit-test runners.
}
