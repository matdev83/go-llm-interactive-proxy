package taskrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type OutputMode uint8

const (
	Stream OutputMode = iota
	Capture
)

type Request struct {
	Argv           []string
	Dir            string
	Env            []string
	ClearEnv       bool
	Timeout        time.Duration
	Context        context.Context
	Output         OutputMode
	StreamOut      io.Writer
	StreamErr      io.Writer
	StdoutLimit    int
	StderrLimit    int
	AggregateLimit int
	HeadLimit      int
	TailLimit      int
	Label          string
	Redactions     []string
}

type Kind string

const (
	Success          Kind = "success"
	ChildFailure     Kind = "child_failure"
	DeadlineExceeded Kind = "deadline_exceeded"
	StartFailure     Kind = "start_failure"
	CleanupFailure   Kind = "cleanup_failure"
	InvalidRequest   Kind = "invalid_request"
)

type CleanupResult struct {
	Attempted bool
	Err       error
}

type Result struct {
	Kind            Kind
	ExitCode        int
	Label           string
	Dir             string
	DurationClass   string
	Elapsed         time.Duration
	Accounting      ProcessAccounting
	AccountingErr   error
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
	Cleanup         CleanupResult
	Err             error
}

const (
	defaultStreamLimit = 64 * 1024
	defaultAggregate   = 256 * 1024
	defaultHead        = 32 * 1024
	defaultTail        = 32 * 1024
)

func Run(ctx context.Context, req Request) (result Result) {
	started := time.Now()
	result = Result{Label: req.Label}
	defer func() {
		if result.Elapsed == 0 {
			result.Elapsed = time.Since(started)
		}
	}()
	if ctx == nil {
		return invalid(result, "nil caller context")
	}
	if req.Context != nil {
		ctx = req.Context
	}
	if err := normalizeWorkingDirectory(&req); err != nil {
		return invalid(result, err.Error())
	}
	if err := validate(&req); err != nil {
		return invalid(result, err.Error())
	}
	if err := ctx.Err(); err != nil {
		result.Kind, result.Err = DeadlineExceeded, err
		result.DurationClass = durationClass(time.Since(started), req.Timeout)
		return result
	}
	result.Dir = req.Dir
	limits := limitsFor(req)
	env, err := childEnvironment(req)
	if err != nil {
		return invalid(result, err.Error())
	}
	childCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	cmd := exec.Command(req.Argv[0], req.Argv[1:]...)
	cmd.Dir = req.Dir
	cmd.Env = env
	adapter, err := newProcessAdapter(cmd)
	if err != nil {
		return fail(result, StartFailure, err)
	}

	var stdout, stderr io.ReadCloser
	if req.Output == Capture {
		stdout, err = cmd.StdoutPipe()
		if err != nil {
			_ = adapter.close()
			return fail(result, StartFailure, err)
		}
		stderr, err = cmd.StderrPipe()
		if err != nil {
			_ = stdout.Close()
			_ = adapter.close()
			return fail(result, StartFailure, err)
		}
	} else {
		cmd.Stdout = writerOrDiscard(req.StreamOut)
		cmd.Stderr = writerOrDiscard(req.StreamErr)
	}
	if err := adapter.start(); err != nil {
		_ = adapter.close()
		return fail(result, StartFailure, err)
	}
	if startupErr := adapter.startupCleanupError(); startupErr != nil {
		result.Cleanup.Attempted = true
		result.Cleanup.Err = startupErr
	}

	if req.Output == Capture {
		var wg sync.WaitGroup
		budget := &captureBudget{remaining: limits.aggregate}
		outBuf := newBoundedBuffer(limits.stdout, limits.head, limits.tail, budget)
		errBuf := newBoundedBuffer(limits.stderr, limits.head, limits.tail, budget)
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = io.Copy(outBuf, stdout) }()
		go func() { defer wg.Done(); _, _ = io.Copy(errBuf, stderr) }()
		result = waitForProcess(childCtx, cmd, adapter, result, &wg, stdout, stderr, started, req.Timeout)
		result.Stdout, result.StdoutTruncated = outBuf.Bytes(), outBuf.Truncated()
		result.Stderr, result.StderrTruncated = errBuf.Bytes(), errBuf.Truncated()
	} else {
		result = waitForProcess(childCtx, cmd, adapter, result, nil, nil, nil, started, req.Timeout)
	}
	redactResult(&result, req.Redactions)
	return result
}

type outputLimits struct{ stdout, stderr, head, tail, aggregate int }

func limitsFor(req Request) outputLimits {
	limits := outputLimits{req.StdoutLimit, req.StderrLimit, req.HeadLimit, req.TailLimit, req.AggregateLimit}
	if limits.stdout == 0 {
		limits.stdout = defaultStreamLimit
	}
	if limits.stderr == 0 {
		limits.stderr = defaultStreamLimit
	}
	if limits.head == 0 {
		limits.head = defaultHead
	}
	if limits.tail == 0 {
		limits.tail = defaultTail
	}
	if limits.aggregate == 0 {
		limits.aggregate = defaultAggregate
	}
	return limits
}

func validate(req *Request) error {
	if len(req.Argv) == 0 || req.Argv[0] == "" {
		return errors.New("argv must contain an executable")
	}
	if req.Timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if req.Output != Stream && req.Output != Capture {
		return errors.New("invalid output mode")
	}
	if req.Dir != "" {
		info, err := os.Stat(req.Dir)
		if err != nil {
			return fmt.Errorf("stat cwd: %w", err)
		}
		if !info.IsDir() {
			return errors.New("cwd is not a directory")
		}
	}
	if req.StdoutLimit < 0 || req.StderrLimit < 0 || req.AggregateLimit < 0 || req.HeadLimit < 0 || req.TailLimit < 0 {
		return errors.New("output limits cannot be negative")
	}
	if req.Output == Capture && (req.StdoutLimit > 0 && req.HeadLimit+req.TailLimit > req.StdoutLimit || req.StderrLimit > 0 && req.HeadLimit+req.TailLimit > req.StderrLimit) {
		return errors.New("head and tail limits exceed stream limit")
	}
	for _, value := range req.Env {
		if !strings.Contains(value, "=") || strings.SplitN(value, "=", 2)[0] == "" {
			return fmt.Errorf("invalid environment entry %q", value)
		}
	}
	return nil
}

func normalizeWorkingDirectory(req *Request) error {
	if req.Dir == "" {
		return nil
	}
	abs, err := filepath.Abs(req.Dir)
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	req.Dir = abs
	return nil
}

func childEnvironment(req Request) ([]string, error) {
	var env []string
	if !req.ClearEnv {
		env = append(env, os.Environ()...)
	}
	for _, override := range req.Env {
		key := strings.SplitN(override, "=", 2)[0]
		found := false
		for i, current := range env {
			if strings.HasPrefix(current, key+"=") {
				env[i], found = override, true
				break
			}
		}
		if !found {
			env = append(env, override)
		}
	}
	return env, nil
}

func writerOrDiscard(writer io.Writer) io.Writer {
	if writer == nil {
		return io.Discard
	}
	return writer
}

func invalid(result Result, message string) Result {
	result.Kind, result.Err = InvalidRequest, errors.New(message)
	return result
}

func fail(result Result, kind Kind, err error) Result {
	result.Kind, result.Err = kind, err
	return result
}

// waitForProcess coordinates process termination, output drain completion, and timeout handling.
// The capture drain WaitGroup is awaited before cmd.Wait() to guarantee that all stdout/stderr
// data in flight is fully copied into memory buffers before the process handles and pipes are torn down.
// On deadline expiration or cancellation, the process adapter is killed and stdout/stderr pipes are
// closed immediately to unblock pending read operations and prevent capture stalls during teardown.
func waitForProcess(ctx context.Context, cmd *exec.Cmd, adapter processAdapter, result Result, drains *sync.WaitGroup, stdout, stderr io.ReadCloser, started time.Time, timeout time.Duration) Result {
	waitDone := make(chan error, 1)
	go func() {
		if drains != nil {
			drains.Wait()
		}
		waitDone <- cmd.Wait()
	}()
	var waitErr error
	select {
	case waitErr = <-waitDone:
	case <-ctx.Done():
		result.Kind = DeadlineExceeded
		result.Err = ctx.Err()
		result.Cleanup.Attempted = true
		result.Cleanup.Err = adapter.kill()
		if stdout != nil {
			_ = stdout.Close()
		}
		if stderr != nil {
			_ = stderr.Close()
		}
		waitErr = <-waitDone
	}
	if stdout != nil {
		_ = stdout.Close()
	}
	if stderr != nil {
		_ = stderr.Close()
	}
	result.Accounting, result.AccountingErr = adapter.accounting()
	if closeErr := adapter.close(); closeErr != nil && result.Cleanup.Err == nil {
		result.Cleanup.Err = closeErr
	}
	if result.Kind == "" {
		result.ExitCode = exitCode(waitErr)
		if waitErr == nil {
			result.Kind = Success
		} else {
			result.Kind = ChildFailure
			result.Err = waitErr
		}
	}
	if result.Kind == DeadlineExceeded || result.Kind == ChildFailure {
		result.ExitCode = exitCode(waitErr)
	}
	if result.Cleanup.Err != nil && result.Kind == Success {
		result.Kind = CleanupFailure
	}
	result.Elapsed = time.Since(started)
	result.DurationClass = durationClass(result.Elapsed, timeout)
	return result
}

func durationClass(elapsed, timeout time.Duration) string {
	if elapsed < timeout/3 {
		return "fast"
	}
	if elapsed >= timeout*8/10 {
		return "near_deadline"
	}
	return "normal"
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 0
}

func redactResult(result *Result, secrets []string) {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		result.Stdout = bytes.ReplaceAll(result.Stdout, []byte(secret), []byte("[REDACTED]"))
		result.Stderr = bytes.ReplaceAll(result.Stderr, []byte(secret), []byte("[REDACTED]"))
	}
}

type captureBudget struct {
	mu        sync.Mutex
	remaining int
}

type boundedBuffer struct {
	limit, head, tail int
	budget            *captureBudget
	headBuf, tailBuf  []byte
	total             int
	truncated         bool
}

func newBoundedBuffer(limit, head, tail int, budget *captureBudget) *boundedBuffer {
	return &boundedBuffer{
		limit:  limit,
		head:   head,
		tail:   tail,
		budget: budget,
	}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.total += len(p)
	if b.total > b.limit {
		b.truncated = true
	}
	b.budget.mu.Lock()
	allowed := min(len(p), b.budget.remaining)
	b.budget.remaining -= allowed
	b.budget.mu.Unlock()
	if allowed == 0 {
		b.truncated = true
		return len(p), nil
	}
	head := min(b.head-len(b.headBuf), allowed)
	b.headBuf = append(b.headBuf, p[:head]...)
	remaining := p[head:allowed]
	tailCapacity := min(b.tail, b.limit-len(b.headBuf))
	if len(remaining) > 0 && tailCapacity > 0 {
		b.tailBuf = append(b.tailBuf, remaining...)
		if len(b.tailBuf) > tailCapacity {
			b.tailBuf = b.tailBuf[len(b.tailBuf)-tailCapacity:]
		}
	}
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte {
	return append(append([]byte(nil), b.headBuf...), b.tailBuf...)
}
func (b *boundedBuffer) Truncated() bool { return b.truncated }
