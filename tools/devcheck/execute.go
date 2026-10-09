package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/tools/taskrunner"
)

const commandTimeout = 30 * time.Minute

func withAnalyzerBudget(root string, command []string) []string {
	if runtime.GOOS == "windows" || len(command) == 0 || command[0] != "golangci-lint" {
		return command
	}
	return append([]string{"bash", filepath.Join(root, "scripts", "go-dev-guard.sh"), "--lip-resource-run"}, command...)
}

// execute delegates process-tree ownership to taskrunner. The parser is joined
// before returning, including malformed telemetry and interruption paths.
func execute(ctx context.Context, cmd *exec.Cmd, test bool, output io.Writer) (testStats, error) {
	if err := ctx.Err(); err != nil {
		return testStats{}, err
	}
	timeout := commandTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
		if timeout <= 0 {
			return testStats{}, context.DeadlineExceeded
		}
	}
	request := taskrunner.Request{
		Argv: cmd.Args, Dir: cmd.Dir, Env: cmd.Env, ClearEnv: cmd.Env != nil,
		Timeout: timeout, Output: taskrunner.Stream, StreamOut: output, StreamErr: cmd.Stderr,
	}
	var stats testStats
	var parseErr error
	var reader *io.PipeReader
	var writer *io.PipeWriter
	var parsed chan struct{}
	if test {
		reader, writer = io.Pipe()
		parsed = make(chan struct{})
		request.StreamOut = writer
		go func() {
			defer close(parsed)
			defer func() { _ = reader.Close() }()
			stats, parseErr = consumeTests(reader, output)
			if parseErr != nil {
				_, _ = io.Copy(io.Discard, reader)
			}
		}()
	}
	result := taskrunner.Run(ctx, request)
	if test {
		_ = writer.Close()
		<-parsed
	}
	return stats, errors.Join(parseErr, result.Err, result.Cleanup.Err, result.AccountingErr)
}

func commandOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	var output bytes.Buffer
	_, err := execute(ctx, cmd, false, &output)
	return output.Bytes(), err
}
