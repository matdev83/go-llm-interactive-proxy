package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/tools/devcheck/internal/testscope"
)

type failedDiagnosticWriter struct{}

func (failedDiagnosticWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestPlanReportingFailureIsNotSuccess(t *testing.T) {
	t.Parallel()
	if err := runTestPlan(t.TempDir(), testscope.Plan{}, testPlanOptions{jobs: 1, repeat: 1, dry: true}, io.Discard, failedDiagnosticWriter{}); err == nil {
		t.Fatal("ignored plan-reporting failure")
	}
}

func TestChangedScopeRejectsManualAndNonTestOptions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ task, module, packages string }{
		{"test", "connectors/one", ""},
		{"test", ".", "./base"},
		{"build", ".", ""},
	} {
		if err := validateChangedScope(tc.task, tc.module, tc.packages); err == nil {
			t.Errorf("accepted automatic scope with %+v", tc)
		}
	}
}

func TestPlanOnlyDoesNotExecuteOrValidateTestModules(t *testing.T) {
	t.Parallel()
	plan := testscope.Plan{Base: "base", Modules: []testscope.Module{{Directory: "missing", Packages: []string{"./..."}}}}
	var output, diagnostics strings.Builder
	err := runTestPlan(t.TempDir(), plan, testPlanOptions{jobs: 1, repeat: 2, dry: true}, &output, &diagnostics)
	if err != nil || !strings.Contains(diagnostics.String(), "plan-only") || output.Len() != 0 {
		t.Fatalf("dry-run err=%v output=%s diagnostics=%s", err, output.String(), diagnostics.String())
	}
}

func TestSelectedExecutionRepeatAndFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "repeat", true: "failure"}[failure], func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/execution\n\ngo 1.26.0\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			body := "package execution\nimport \"testing\"\nfunc TestExecution(t *testing.T) { t.Log(\"ran selected test\") }\n"
			if failure {
				body = strings.ReplaceAll(body, "t.Log", "t.Fatal")
			}
			if err := os.WriteFile(filepath.Join(root, "execution_test.go"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			plan := testscope.Plan{Modules: []testscope.Module{{Directory: ".", Packages: []string{"."}}}}
			var output, diagnostics strings.Builder
			err := runTestPlan(root, plan, testPlanOptions{jobs: 1, repeat: 2, fresh: true}, &output, &diagnostics)
			if (err != nil) != failure {
				t.Fatalf("failure=%v err=%v", failure, err)
			}
			if !failure && strings.Count(diagnostics.String(), "passed=1") != 2 {
				t.Fatalf("repeat did not execute the same plan: %s", diagnostics.String())
			}
			if failure && strings.Contains(diagnostics.String(), "[2/2]") {
				t.Fatal("continued after failed tests")
			}
		})
	}
}
