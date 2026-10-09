package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/tools/devcheck/internal/evidence"
)

// Claim: a recorded step's log file holds the command's own output. A manifest
// that names a log nobody wrote is worse than no log, because it invites the
// reader to trust evidence that does not exist.
func TestRecorderTeesCommandOutputToLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	recorder := newEvidenceRecorder(t.Context(), path, "build", evidence.Scope{Kind: "explicit", Module: "."}, t.TempDir())
	var streamed strings.Builder

	step := recorder.start("module-a", []string{"sh", "-c", "printf streamed"}, t.TempDir(), &streamed)
	cmd := exec.Command("sh", "-c", "printf streamed")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("probe command: %v", err)
	}
	if _, err := step.output.Write(output); err != nil {
		t.Fatalf("write to step output: %v", err)
	}
	recorder.finishRun(step, nil, nil)
	if err := recorder.finish(nil); err != nil {
		t.Fatalf("finish: %v", err)
	}

	if streamed.String() != "streamed" {
		t.Errorf("caller stream = %q, want the command output to pass through unchanged", streamed.String())
	}
	manifest := readManifest(t, path)
	if len(manifest.Steps) != 1 {
		t.Fatalf("steps = %+v, want one recorded step", manifest.Steps)
	}
	logged, err := os.ReadFile(manifest.Steps[0].LogPath)
	if err != nil {
		t.Fatalf("recorded log %q: %v", manifest.Steps[0].LogPath, err)
	}
	if string(logged) != "streamed" {
		t.Errorf("log = %q, want the command output", logged)
	}
	if manifest.Steps[0].Result != evidence.OutcomePassed || manifest.Steps[0].ExitCode != 0 {
		t.Errorf("step = %+v, want a passed step with exit code 0", manifest.Steps[0])
	}
}

// Claim: a failing command records a non-zero exit code and makes the manifest
// a failure, so a red run cannot be filed as evidence of anything.
func TestRecorderRecordsFailedCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	recorder := newEvidenceRecorder(t.Context(), path, "build", evidence.Scope{Kind: "explicit", Module: "."}, t.TempDir())

	step := recorder.start("go-build", []string{"sh", "-c", "exit 3"}, t.TempDir(), os.Stdout)
	runErr := exec.Command("sh", "-c", "exit 3").Run()
	recorder.finishRun(step, &evidence.Stats{Passed: 4, Failed: 2}, runErr)
	if err := recorder.finish(runErr); err != nil {
		t.Fatalf("finish: %v", err)
	}

	manifest := readManifest(t, path)
	if manifest.Outcome != evidence.OutcomeFailed {
		t.Errorf("outcome = %q, want %q", manifest.Outcome, evidence.OutcomeFailed)
	}
	step0 := manifest.Steps[0]
	if step0.Result != evidence.OutcomeFailed || step0.ExitCode != 3 {
		t.Errorf("step = %+v, want a failed step with exit code 3", step0)
	}
	if manifest.Totals.TestsPassed != 4 || manifest.Totals.TestsFailed != 2 {
		t.Errorf("totals = %+v, want the failed step's counters preserved", manifest.Totals)
	}
}

// Claim: a blocked condition is reported as blocked even when a command also
// failed. The blocker is the actionable fact; the command error is downstream.
func TestRecorderBlockOutranksCommandFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	recorder := newEvidenceRecorder(t.Context(), path, "lint", evidence.Scope{Kind: "explicit", Module: "."}, t.TempDir())
	recorder.block("required tool golangci-lint is unavailable")
	recorder.block("a later condition that adds nothing")

	runErr := exec.Command("sh", "-c", "exit 1").Run()
	if err := recorder.finish(runErr); err != nil {
		t.Fatalf("finish: %v", err)
	}

	manifest := readManifest(t, path)
	if manifest.Outcome != evidence.OutcomeBlocked {
		t.Errorf("outcome = %q, want %q", manifest.Outcome, evidence.OutcomeBlocked)
	}
	if !strings.Contains(manifest.FailureReason, "golangci-lint") {
		t.Errorf("failure reason = %q, want the first blocking condition", manifest.FailureReason)
	}
}

// Claim: without -evidence the recorder is absent and command output is
// untouched. Recording must be strictly opt-in.
func TestRecorderAbsentWithoutPath(t *testing.T) {
	recorder := newEvidenceRecorder(t.Context(), "", "test", evidence.Scope{Kind: "explicit"}, t.TempDir())
	if recorder != nil {
		t.Fatalf("recorder = %+v, want none", recorder)
	}
	var sink strings.Builder
	step := recorder.start("label", []string{"true"}, t.TempDir(), &sink)
	if _, err := step.output.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if sink.String() != "x" {
		t.Errorf("sink = %q, want the command output unchanged", sink.String())
	}
	if step.logPath != "" {
		t.Errorf("log path = %q, want none recorded", step.logPath)
	}
	recorder.block("ignored")
	recorder.finishRun(step, nil, nil)
	if err := recorder.finish(nil); err != nil {
		t.Fatalf("finish with no manifest: %v", err)
	}
}

// Claim: a non-zero exit code is only reported for a real process failure. A
// command that never started must not look like it exited 0.
func TestExitCodeReportsUnstartedCommand(t *testing.T) {
	if got := exitCode(nil); got != 0 {
		t.Errorf("exitCode(nil) = %d, want 0", got)
	}
	if got := exitCode(errors.New("spawn failed")); got != -1 {
		t.Errorf("exitCode(plain error) = %d, want -1", got)
	}
}

// Claim: a label with separators cannot escape the evidence directory.
func TestSanitizeLogLabelProducesSingleFileName(t *testing.T) {
	for _, tc := range []struct {
		label string
		want  string
	}{
		{label: "module-a", want: "module-a"},
		{label: "./connectors/x [tags=precommit]", want: "connectors-x--tags-precommit"},
		{label: "///", want: "command"},
	} {
		if got := sanitizeLogLabel(tc.label); got != tc.want {
			t.Errorf("sanitizeLogLabel(%q) = %q, want %q", tc.label, got, tc.want)
		}
		if strings.ContainsAny(sanitizeLogLabel(tc.label), `/\`) {
			t.Errorf("sanitizeLogLabel(%q) kept a path separator", tc.label)
		}
	}
}

func readManifest(t *testing.T, path string) *evidence.Manifest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifest := &evidence.Manifest{}
	if err := json.Unmarshal(data, manifest); err != nil {
		t.Fatalf("decode manifest: %v\n%s", err, data)
	}
	return manifest
}

func TestRecorder_MissingLogsCannotReturnSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	recorder := newEvidenceRecorder(t.Context(), path, "test", evidence.Scope{Kind: "explicit"}, t.TempDir())
	recorder.block("cannot retain full command logs")
	if err := recorder.finish(nil); err == nil {
		t.Fatal("missing log evidence returned success")
	}
	manifest := readManifest(t, path)
	if manifest.Outcome != evidence.OutcomeBlocked || manifest.FailureReason != "cannot retain full command logs" {
		t.Fatalf("lost evidence blocker: %+v", manifest)
	}
}
