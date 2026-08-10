package qa

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
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
	script := repositoryFile(t, "scripts", "ci-scope.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Bash is not guaranteed on Windows; the remote Ubuntu workflow executes
		// the authoritative shell self-test.
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", script, "--self-test")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("scope self-test failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "OK: CI scope self-test") {
		t.Fatalf("scope self-test marker missing: %q", output)
	}
}
