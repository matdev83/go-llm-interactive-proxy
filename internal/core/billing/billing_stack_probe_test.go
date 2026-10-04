//go:build integration

package billing_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	drStackChildEnv         = "LIP_BILLING_STACK_CHILD"
	drStackChildFlat        = "flat"
	drStackChildRecursive   = "recursive"
	drStackChildMutantSmall = "mutant-small"
	drStackChildMutantLarge = "mutant-large"
	drStackChildExplicitGC  = "explicit-gc"

	drStackDepthPrefix = "depth-"
	// Preserve the original 64 KiB depth-spread contract; this is a
	// calibrated difference between fresh isolated child measurements, not an
	// absolute stack ceiling.
	drStackSpreadLimit = 64 << 10
	drStackOutputLimit = 16 << 10
)

type drBoundedOutput struct {
	bytes.Buffer
	truncated bool
}

func (b *drBoundedOutput) Write(p []byte) (int, error) {
	remaining := drStackOutputLimit - b.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

func drStackChildMode() string { return os.Getenv(drStackChildEnv) }

func drRunStackChildProcess(t *testing.T, mode, testPattern string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run", testPattern, "-test.v")
	cmd.Env = append(os.Environ(), drStackChildEnv+"="+mode)
	var captured drBoundedOutput
	cmd.Stdout = &captured
	cmd.Stderr = &captured
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("stack certificate child %q did not finish: %v\n%s", mode, ctx.Err(), drSanitizeStackChildOutput(captured.String(), captured.truncated))
	}
	return drSanitizeStackChildOutput(captured.String(), captured.truncated), err
}

func drSanitizeStackChildOutput(output string, truncated bool) string {
	var kept []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		known := false
		for _, prefix := range []string{"DR_STACK_RESULT ", "DR_STACK_MUTANT ", "DR_STACK_DIAGNOSTIC "} {
			if strings.HasPrefix(line, prefix) {
				kept = append(kept, line)
				known = true
				break
			}
		}
		if strings.HasPrefix(line, "DR_") && !known {
			kept = append(kept, "DR_STACK_DIAGNOSTIC unknown_record")
		}
	}
	if strings.Contains(output, "goroutine stack exceeds") && strings.Contains(output, "stack overflow") {
		kept = append(kept, "DR_STACK_DIAGNOSTIC stack_overflow")
	}
	if truncated {
		kept = append(kept, "DR_STACK_DIAGNOSTIC output_truncated")
	}
	sanitized := strings.Join(kept, "\n")
	if len(sanitized) > drStackOutputLimit {
		return sanitized[:drStackOutputLimit]
	}
	return sanitized
}

func TestDRSanitizeStackChildOutputIsBoundedAndRetainsUnknownProtocol(t *testing.T) {
	t.Parallel()
	output := strings.Repeat("DR_STACK_OTHER "+strings.Repeat("x", 256)+"\n", drStackOutputLimit)
	sanitized := drSanitizeStackChildOutput(output, true)
	if len(sanitized) > drStackOutputLimit {
		t.Fatalf("sanitized child output length=%d, exceeds limit %d", len(sanitized), drStackOutputLimit)
	}
	if !strings.Contains(sanitized, "DR_STACK_DIAGNOSTIC unknown_record") {
		t.Fatalf("sanitized child output dropped unknown protocol diagnostic")
	}
}

func drRunStackChild(t *testing.T, mode string) {
	t.Helper()
	switch {
	case strings.HasPrefix(mode, drStackDepthPrefix):
		depth, err := strconv.Atoi(strings.TrimPrefix(mode, drStackDepthPrefix))
		if err != nil || depth <= 0 {
			t.Fatalf("invalid stack certificate depth %q", mode)
		}
		drRunDeepChainBoundedness(t, depth)
	case mode == drStackChildFlat:
		drIterativeStackProbe(8192)
	case mode == drStackChildRecursive:
		// This standalone negative control deliberately uses a small runtime
		// ceiling; the production certificate below uses retained growth instead.
		debug.SetMaxStack(64 << 10)
		drRecursiveStackProbe(8192, func() {})
		t.Fatal("recursive stack control returned")
	case mode == drStackChildMutantSmall:
		drRunRateStackMutant(t, false)
	case mode == drStackChildMutantLarge:
		drRunRateStackMutant(t, true)
	case mode == drStackChildExplicitGC:
		drRunExplicitGCMeasurement(t)
	default:
		t.Fatalf("unknown stack certificate child mode %q", mode)
	}
}

//go:noinline
func drIterativeStackProbe(depth int) {
	total := 0
	for i := 0; i < depth; i++ {
		total += i
	}
	if total < 0 {
		panic("iterative stack probe overflowed")
	}
}

//go:noinline
func drRecursiveStackProbe(depth int, atPeak func()) {
	if depth == 0 {
		atPeak()
		return
	}
	drRecursiveStackProbe(depth-1, atPeak)
}

func drAssertStackOverflow(t *testing.T, output string) {
	t.Helper()
	if !strings.Contains(output, "DR_STACK_DIAGNOSTIC stack_overflow") {
		t.Fatalf("recursive child failed without the expected stack-overflow diagnostic: %s", output)
	}
}

func TestBillingStackProbeControls(t *testing.T) {
	if mode := drStackChildMode(); mode != "" {
		drRunStackChild(t, mode)
		return
	}
	if output, err := drRunStackChildProcess(t, drStackChildFlat, "^TestBillingStackProbeControls$"); err != nil {
		t.Fatalf("iterative stack child failed: %v\n%s", err, output)
	}
	output, err := drRunStackChildProcess(t, drStackChildRecursive, "^TestBillingStackProbeControls$")
	if err == nil {
		t.Fatalf("recursive child unexpectedly completed: %s", output)
	}
	drAssertStackOverflow(t, output)
}
