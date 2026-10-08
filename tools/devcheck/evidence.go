package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/tools/devcheck/internal/evidence"
)

// evidenceRecorder writes the manifest requested with -evidence and tees each
// command's output into its own log file beside it.
//
// The tee is the point of the log records: a manifest that names a log nobody
// wrote is worse than no log, because it invites the reader to trust evidence
// that does not exist. Without -evidence the recorder is nil and command
// output is untouched.
type evidenceRecorder struct {
	manifest    *evidence.Manifest
	path        string
	dir         string
	sequence    int
	blockReason string
}

func newEvidenceRecorder(path, task string, scope evidence.Scope, workdir string) *evidenceRecorder {
	if path == "" {
		return nil
	}
	absolute := path
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(workdir, absolute)
	}
	manifest := evidence.New(evidence.Input{
		Task:    task,
		Scope:   scope,
		Env:     os.Environ(),
		Workdir: workdir,
	})
	return &evidenceRecorder{
		manifest: manifest,
		path:     absolute,
		dir:      filepath.Dir(absolute),
	}
}

// block records the first infrastructure condition that prevented the check
// from producing a code verdict. Later conditions add nothing: the run already
// cannot claim its scope passed.
func (r *evidenceRecorder) block(reason string) {
	if r == nil || r.blockReason != "" {
		return
	}
	r.blockReason = reason
}

func (r *evidenceRecorder) finish(runErr error) error {
	if r == nil {
		return nil
	}
	r.manifest.Finish(runErr, r.blockReason)
	return r.manifest.Write(r.path)
}

// commandRun is one in-flight command: its log file, the writer that streams
// into it, and the start time used for the recorded duration.
type commandRun struct {
	label     string
	command   []string
	dir       string
	startedAt time.Time
	logPath   string
	log       *os.File
	output    io.Writer
}

// start opens this step's log and returns the writer command output must go to.
// A log that cannot be opened is not fatal: the check still runs, and the step
// simply records no log path.
func (r *evidenceRecorder) start(label string, command []string, dir string, stdout io.Writer) *commandRun {
	run := &commandRun{
		label:     label,
		command:   append([]string(nil), command...),
		dir:       dir,
		startedAt: time.Now(),
		output:    stdout,
	}
	if r == nil {
		return run
	}
	r.sequence++
	name := fmt.Sprintf("step-%02d-%s.log", r.sequence, sanitizeLogLabel(label))
	path := filepath.Join(r.dir, name)
	log, err := os.Create(path)
	if err != nil {
		r.block(fmt.Sprintf("open step log %s: %v", path, err))
		return run
	}
	run.log = log
	run.logPath = path
	run.output = io.MultiWriter(stdout, log)
	return run
}

// finish records the step result and closes its log. stats is nil for commands
// that do not emit the test event stream.
func (r *evidenceRecorder) finishRun(run *commandRun, stats *evidence.Stats, runErr error) {
	if run == nil {
		return
	}
	if run.log != nil {
		runErr = errors.Join(runErr, run.log.Close())
	}
	if r == nil {
		return
	}
	step := evidence.Step{
		Label:      run.label,
		Command:    run.command,
		WorkingDir: run.dir,
		StartedAt:  run.startedAt,
		DurationMS: time.Since(run.startedAt).Milliseconds(),
		Result:     evidence.OutcomePassed,
		ExitCode:   exitCode(runErr),
		LogPath:    run.logPath,
		Tests:      stats,
	}
	if runErr != nil {
		step.Result = evidence.OutcomeFailed
	}
	r.manifest.Steps = append(r.manifest.Steps, step)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func sanitizeLogLabel(label string) string {
	var b strings.Builder
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	trimmed := strings.Trim(b.String(), "-")
	if trimmed == "" {
		return "command"
	}
	return trimmed
}
