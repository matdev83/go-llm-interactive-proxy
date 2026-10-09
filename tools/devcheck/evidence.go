package main

import (
	"context"
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
	quiet       bool
	report      io.Writer
}

func newEvidenceRecorder(ctx context.Context, path, task string, scope evidence.Scope, workdir string) *evidenceRecorder {
	if path == "" {
		return nil
	}
	absolute := path
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(workdir, absolute)
	}
	manifest := evidence.New(ctx, evidence.Input{
		Task:    task,
		Scope:   scope,
		Env:     os.Environ(),
		Workdir: workdir,
	})
	recorder := &evidenceRecorder{
		manifest: manifest,
		path:     absolute,
		dir:      filepath.Dir(absolute),
	}
	if task == "test" || task == "contracts" {
		if fields, err := splitGoFlags(manifest.Environment.GoFlags); err == nil {
			if pattern, found, err := lastGoFlagsSkip(fields); err == nil && found && pattern != "" {
				recorder.skip(pattern, "inherited GOFLAGS test exclusion")
			}
		}
	}
	return recorder
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
	writeErr := r.manifest.Write(r.path)
	if r.quiet && r.report != nil {
		_, writeSummaryErr := fmt.Fprintf(r.report, "verification=%s evidence=%s\n", r.manifest.Outcome, r.path)
		writeErr = errors.Join(writeErr, writeSummaryErr)
	}
	if runErr == nil && (r.manifest.Outcome == evidence.OutcomeBlocked || r.manifest.Outcome == evidence.OutcomeFailed) {
		writeErr = errors.Join(writeErr, errors.New(r.manifest.FailureReason))
	}
	return writeErr
}

func (r *evidenceRecorder) skip(target, reason string) {
	if r != nil {
		r.manifest.Skips = append(r.manifest.Skips, evidence.Skip{Target: target, Reason: reason})
	}
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
	live      io.Writer
	quiet     bool
	scope     evidence.Selection
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
	module, err := filepath.Rel(r.manifest.Revision.Worktree, dir)
	if err != nil {
		module = dir
	}
	run.scope.Module = filepath.ToSlash(module)
	for _, arg := range command {
		if arg == "." || strings.HasPrefix(arg, "./") {
			run.scope.Packages = append(run.scope.Packages, arg)
		}
	}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		r.block(fmt.Sprintf("create step log directory: %v", err))
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
	run.quiet = r.quiet
	if run.quiet {
		stdout = io.Discard
	}
	run.live = stdout
	run.output = io.MultiWriter(log, stdout)
	return run
}

func (run *commandRun) stdout(test bool) io.Writer {
	if test && run.log != nil {
		return run.live
	}
	return run.output
}

func (run *commandRun) rawLog() io.Writer {
	if run.log == nil {
		return nil
	}
	return run.log
}

func (run *commandRun) stderr(live io.Writer) io.Writer {
	if run.log == nil {
		return live
	}
	if run.quiet {
		live = io.Discard
	}
	return io.MultiWriter(run.log, live)
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
		Scope:      run.scope,
	}
	if runErr != nil {
		step.Result = evidence.OutcomeFailed
	}
	r.manifest.Steps = append(r.manifest.Steps, step)
	if r.quiet && r.report != nil {
		if _, err := fmt.Fprintf(r.report, "%s: %s elapsed=%.3fs scope=%s:%v log=%s\n", step.Label, step.Result, float64(step.DurationMS)/1000, step.Scope.Module, step.Scope.Packages, step.LogPath); err != nil {
			r.block(fmt.Sprintf("write summary: %v", err))
		}
	}
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
