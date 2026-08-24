package taskrunner

import (
	"bytes"
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"
)

func TestRunner_NormalExitCapture(t *testing.T) {
	t.Parallel()
	helper := buildHelper(t)
	req := Request{
		Argv:    []string{helper, "-mode=output", "-size=20"},
		Timeout: 5 * time.Second,
		Output:  Capture,
	}
	result := Run(context.Background(), req)
	if result.Kind != Success {
		t.Fatalf("expected Kind == Success, got %v (err: %v)", result.Kind, result.Err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected ExitCode == 0, got %d", result.ExitCode)
	}
	wantStdout := "OOOOOOOOOOoooooooooo"
	wantStderr := "EEEEEEEEEEeeeeeeeeee"
	if string(result.Stdout) != wantStdout {
		t.Fatalf("expected Stdout %q, got %q", wantStdout, string(result.Stdout))
	}
	if string(result.Stderr) != wantStderr {
		t.Fatalf("expected Stderr %q, got %q", wantStderr, string(result.Stderr))
	}
	if result.StdoutTruncated {
		t.Fatalf("expected StdoutTruncated == false, got true")
	}
	if result.StderrTruncated {
		t.Fatalf("expected StderrTruncated == false, got true")
	}
	if result.Cleanup.Attempted {
		t.Fatalf("expected Cleanup.Attempted == false, got true")
	}
}

func TestRunner_TimeoutPath(t *testing.T) {
	t.Parallel()
	helper := buildHelper(t)
	timeout := 100 * time.Millisecond
	req := Request{
		Argv:    []string{helper, "-mode=sleep"},
		Timeout: timeout,
		Output:  Capture,
	}
	start := time.Now()
	result := Run(context.Background(), req)
	elapsed := time.Since(start)

	if result.Kind != DeadlineExceeded {
		t.Fatalf("expected Kind == DeadlineExceeded, got %v", result.Kind)
	}
	if !result.Cleanup.Attempted {
		t.Fatalf("expected Cleanup.Attempted == true, got false")
	}
	if result.Cleanup.Err != nil {
		t.Fatalf("expected Cleanup.Err == nil, got %v", result.Cleanup.Err)
	}
	if elapsed > 10*timeout {
		t.Fatalf("expected prompt return, elapsed %v exceeded deadline bound", elapsed)
	}
	if result.DurationClass != "near_deadline" {
		t.Fatalf("expected DurationClass == 'near_deadline', got %q", result.DurationClass)
	}
}

func TestRunner_StreamMode(t *testing.T) {
	t.Parallel()
	helper := buildHelper(t)
	var outBuf, errBuf bytes.Buffer
	req := Request{
		Argv:      []string{helper, "-mode=output", "-size=20"},
		Timeout:   5 * time.Second,
		Output:    Stream,
		StreamOut: &outBuf,
		StreamErr: &errBuf,
	}
	result := Run(context.Background(), req)
	if result.Kind != Success {
		t.Fatalf("expected Kind == Success, got %v (err: %v)", result.Kind, result.Err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected ExitCode == 0, got %d", result.ExitCode)
	}
	wantStdout := "OOOOOOOOOOoooooooooo"
	wantStderr := "EEEEEEEEEEeeeeeeeeee"
	if outBuf.String() != wantStdout {
		t.Fatalf("expected StreamOut %q, got %q", wantStdout, outBuf.String())
	}
	if errBuf.String() != wantStderr {
		t.Fatalf("expected StreamErr %q, got %q", wantStderr, errBuf.String())
	}
	if len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatalf("expected empty captured stdout/stderr in Stream mode, got stdout=%q, stderr=%q", result.Stdout, result.Stderr)
	}
	if result.Cleanup.Attempted {
		t.Fatalf("expected Cleanup.Attempted == false in clean stream run")
	}
}

func TestRunner_DrainOrderInvariant(t *testing.T) {
	// Drain-order invariant: proves capture goroutines complete BEFORE cmd.Wait() resolves.
	// We call waitForProcess directly with a synthetic drains WaitGroup.
	// While drains is held (blocked), cmd.Wait() must NOT have executed / completed.
	// In the NEW implementation, drains.Wait() runs BEFORE cmd.Wait(), so cmd.ProcessState remains nil
	// while drains is held.
	// In the OLD implementation, cmd.Wait() ran concurrently in its own goroutine and finished as soon
	// as the child process exited, populating cmd.ProcessState before drains were released.
	helper := buildHelper(t)
	cmd := exec.Command(helper, "-mode=success")
	adapter, err := newProcessAdapter(cmd)
	if err != nil {
		t.Fatalf("newProcessAdapter: %v", err)
	}
	if err := adapter.start(); err != nil {
		t.Fatalf("adapter.start: %v", err)
	}

	var drains sync.WaitGroup
	drains.Add(1)

	done := make(chan Result, 1)
	go func() {
		done <- waitForProcess(context.Background(), cmd, adapter, Result{}, &drains, nil, nil, time.Now(), 10*time.Second)
	}()

	// Allow the child process time to finish execution and exit.
	// Even though the child process has exited at the OS level, cmd.Wait() must not be called
	// until drains.Wait() completes.
	time.Sleep(100 * time.Millisecond)

	if cmd.ProcessState != nil {
		t.Fatalf("regression: cmd.Wait() resolved before drains completed (cmd.ProcessState = %v)", cmd.ProcessState)
	}

	// Release the drains now.
	drains.Done()

	select {
	case res := <-done:
		if res.Kind != Success {
			t.Fatalf("expected Success, got %#v", res)
		}
		if cmd.ProcessState == nil {
			t.Fatal("expected cmd.ProcessState to be populated after waitForProcess completes")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForProcess did not return after drains released")
	}
}
