package main

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/tools/taskrunner"
)

func buildCLIHelper(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "taskrunner-testhelper")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	// Prebuild outside any child timeout. Full-suite load makes `go run` compile
	// time exceed a 100ms deadline budget and flip this CLI contract to exit 1/3.
	cmd := exec.Command("go", "build", "-o", path, "../../testhelper")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build test helper: %v\n%s", err, output)
	}
	return path
}

func TestResultExitCodeMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		kind    taskrunner.Kind
		cleanup bool
		want    int
	}{
		{"success", taskrunner.Success, false, 0},
		{"child_failure", taskrunner.ChildFailure, false, 1},
		{"deadline_exceeded", taskrunner.DeadlineExceeded, false, 2},
		{"start_failure", taskrunner.StartFailure, false, 3},
		{"cleanup_failure", taskrunner.CleanupFailure, false, 3},
		{"child_failure_with_cleanup_error", taskrunner.ChildFailure, true, 3},
		{"invalid_request", taskrunner.InvalidRequest, false, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := taskrunner.Result{Kind: tc.kind}
			if tc.cleanup {
				result.Cleanup = taskrunner.CleanupResult{Attempted: true, Err: errors.New("cleanup failed")}
			}
			if got := resultExitCode(result); got != tc.want {
				t.Fatalf("resultExitCode(%s) = %d, want %d", tc.kind, got, tc.want)
			}
		})
	}
}

func TestRunDeadlineExceededExitCode(t *testing.T) {
	t.Parallel()
	helper := buildCLIHelper(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--timeout", "100ms",
		"--output", "capture",
		"--", helper, "-mode=sleep",
	}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("deadline exceeded exit code = %d, want 2 (approved CLI contract); stderr=%q", code, stderr.String())
	}
}

func TestParseRequestTimeoutErrorDetail(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	_, err := parseRequest([]string{"--timeout", "not-a-duration", "--", "echo"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected an error for an unparsable timeout")
	}
	if !strings.Contains(err.Error(), "time: invalid duration") {
		t.Fatalf("parse duration error detail was lost: %v", err)
	}
}

func TestRunCaptureWritesFailureDiagnosticsOnce(t *testing.T) {
	t.Parallel()
	helper := buildCLIHelper(t)
	const marker = "unique-taskrunner-failure-marker"
	var stdout, stderr bytes.Buffer

	code := run([]string{
		"--timeout", "5s",
		"--output", "capture",
		"--", helper, "-mode=fail-output", "-marker", marker, "-exit", "23",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("capture failure unexpectedly succeeded")
	}
	if got := strings.Count(stderr.String(), marker); got != 1 {
		t.Fatalf("failure marker count = %d, want 1; stderr=%q", got, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("failed capture leaked stdout: %q", stdout.String())
	}
}

func TestRunCaptureWritesSuccessfulStdoutOnce(t *testing.T) {
	t.Parallel()
	helper := buildCLIHelper(t)
	var stdout, stderr bytes.Buffer

	code := run([]string{
		"--timeout", "5s",
		"--output", "capture",
		"--", helper, "-mode=success",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("capture succeeded with exit code %d: %q", code, stderr.String())
	}
	if got := stdout.String(); got != "success\n" {
		t.Fatalf("stdout = %q, want one child output", got)
	}
}
