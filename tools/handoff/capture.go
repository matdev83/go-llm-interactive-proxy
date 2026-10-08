package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
)

// captureCommand runs explicit argv, saves real output/status, and refuses to
// attribute a check to source that changed during execution.
func captureCommand(ctx context.Context, repo, log, purpose string, argv []string, output, diagnostics io.Writer) error {
	if !slices.Contains([]string{"red", "verification", "baseline"}, purpose) {
		return errors.New("unknown evidence purpose")
	}
	log, err := filepath.Abs(log)
	if err != nil {
		return err
	}
	before, err := snapshot(ctx, repo)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(log, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = repo
	cmd.Stdout = io.MultiWriter(file, diagnostics)
	cmd.Stderr = cmd.Stdout
	runErr := cmd.Run()
	closeErr := file.Close()
	after, stateErr := snapshot(ctx, repo)
	if err := errors.Join(closeErr, stateErr); err != nil {
		return err
	}
	if before != after {
		return errors.New("source changed during execution; retained log is not current-source evidence")
	}
	code := 0
	if runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) {
			return runErr
		}
		code = exit.ExitCode()
	}
	record := struct {
		Source  SourceState `json:"source"`
		Command Command     `json:"command"`
	}{Source: after, Command: Command{Purpose: purpose, Argv: argv, ExitCode: &code, Evidence: log}}
	return errors.Join(json.NewEncoder(output).Encode(record), runErr)
}
