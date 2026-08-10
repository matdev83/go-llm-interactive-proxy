package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskrunnerProcessTreeWorkflow_RemoteNativeMatrix(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	workflowPath := filepath.Join(root, ".github", "workflows", "taskrunner-process-tree.yml")
	contents, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read %s: %v", workflowPath, err)
	}
	workflow := string(contents)
	for _, runner := range []string{"ubuntu-latest", "macos-latest", "windows-latest"} {
		if !strings.Contains(workflow, runner) {
			t.Fatalf("workflow must schedule %s", runner)
		}
	}
	for _, needle := range []string{
		"go test -count=1 -timeout=10m -run 'TestProcessTree_' ./tools/taskrunner",
		"runner.os != 'Windows'",
		"runner.os == 'Windows'",
		"LIP_RUN_WINDOWS_PROCESS_TREE_TESTS: \"1\"",
		"tools/taskrunner/**",
		"pull_request:",
		"id: scope",
		"needs.scope.outputs.run_suite",
		"Report unrelated PR bypass",
		"Taskrunner process-tree suite bypassed: no relevant files changed.",
		"Fail closed if scope detection failed",
	} {
		if !strings.Contains(workflow, needle) {
			t.Errorf("workflow missing remote process-tree contract %q", needle)
		}
	}

	testSource := readTaskrunnerRepositoryFile(t, "tools", "taskrunner", "process_tree_windows_test.go")
	for _, needle := range []string{
		"LIP_RUN_WINDOWS_PROCESS_TREE_TESTS",
		"Windows Job Object process-tree tests run only in remote native QA",
	} {
		if !strings.Contains(testSource, needle) {
			t.Errorf("Windows taskrunner tests missing remote-only guard %q", needle)
		}
	}
}

func readTaskrunnerRepositoryFile(t *testing.T, parts ...string) string {
	t.Helper()
	root := repoRoot(t)
	contents, err := os.ReadFile(filepath.Join(append([]string{root}, parts...)...))
	if err != nil {
		t.Fatalf("read taskrunner contract file: %v", err)
	}
	return string(contents)
}
