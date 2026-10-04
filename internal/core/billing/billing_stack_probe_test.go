//go:build integration

package billing_test

import (
	"bytes"
	"context"
	"math"
	"os"
	"os/exec"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
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

func TestDRStackResultParserRejectsMalformedRecords(t *testing.T) {
	t.Parallel()
	const valid = "DR_STACK_RESULT depth=8192 stack_growth=65536 allocs_per_node=1.25 bytes_per_node=2.5 fingerprint=0123456789abcdef wall_ms=3"
	tests := []struct {
		name   string
		output string
		valid  bool
	}{
		{name: "valid", output: valid, valid: true},
		{name: "missing depth", output: "DR_STACK_RESULT stack_growth=65536 allocs_per_node=1.25 bytes_per_node=2.5 fingerprint=0123456789abcdef wall_ms=3"},
		{name: "missing stack growth", output: "DR_STACK_RESULT depth=8192 allocs_per_node=1.25 bytes_per_node=2.5 fingerprint=0123456789abcdef wall_ms=3"},
		{name: "missing allocations", output: "DR_STACK_RESULT depth=8192 stack_growth=65536 bytes_per_node=2.5 fingerprint=0123456789abcdef wall_ms=3"},
		{name: "missing bytes", output: "DR_STACK_RESULT depth=8192 stack_growth=65536 allocs_per_node=1.25 fingerprint=0123456789abcdef wall_ms=3"},
		{name: "missing fingerprint", output: "DR_STACK_RESULT depth=8192 stack_growth=65536 allocs_per_node=1.25 bytes_per_node=2.5 wall_ms=3"},
		{name: "missing wall time", output: "DR_STACK_RESULT depth=8192 stack_growth=65536 allocs_per_node=1.25 bytes_per_node=2.5 fingerprint=0123456789abcdef"},
		{name: "negative integer", output: "DR_STACK_RESULT depth=8192 stack_growth=-1 allocs_per_node=1.25 bytes_per_node=2.5 fingerprint=0123456789abcdef wall_ms=3"},
		{name: "negative allocation", output: "DR_STACK_RESULT depth=8192 stack_growth=65536 allocs_per_node=-1 bytes_per_node=2.5 fingerprint=0123456789abcdef wall_ms=3"},
		{name: "nan allocation", output: "DR_STACK_RESULT depth=8192 stack_growth=65536 allocs_per_node=NaN bytes_per_node=2.5 fingerprint=0123456789abcdef wall_ms=3"},
		{name: "positive infinity bytes", output: "DR_STACK_RESULT depth=8192 stack_growth=65536 allocs_per_node=1.25 bytes_per_node=+Inf fingerprint=0123456789abcdef wall_ms=3"},
		{name: "negative infinity bytes", output: "DR_STACK_RESULT depth=8192 stack_growth=65536 allocs_per_node=1.25 bytes_per_node=-Inf fingerprint=0123456789abcdef wall_ms=3"},
		{name: "unknown field", output: "DR_STACK_RESULT depth=8192 stack_growth=65536 allocs_per_node=1.25 bytes_per_node=2.5 fingerprint=0123456789abcdef wall_ms=3 extra=1"},
		{name: "duplicate field", output: "DR_STACK_RESULT depth=8192 stack_growth=65536 stack_growth=65536 allocs_per_node=1.25 bytes_per_node=2.5 fingerprint=0123456789abcdef wall_ms=3"},
		{name: "duplicate record", output: valid + "\n" + valid},
		{name: "truncated diagnostic", output: valid + "\nDR_STACK_DIAGNOSTIC output_truncated"},
		{name: "unknown record", output: valid + "\nDR_STACK_OTHER depth=8192"},
		{name: "negative wall time", output: "DR_STACK_RESULT depth=8192 stack_growth=65536 allocs_per_node=1.25 bytes_per_node=2.5 fingerprint=0123456789abcdef wall_ms=-1"},
		{name: "invalid wall time", output: "DR_STACK_RESULT depth=8192 stack_growth=65536 allocs_per_node=1.25 bytes_per_node=2.5 fingerprint=0123456789abcdef wall_ms=NaN"},
		{name: "mismatched depth", output: "DR_STACK_RESULT depth=800 stack_growth=65536 allocs_per_node=1.25 bytes_per_node=2.5 fingerprint=0123456789abcdef wall_ms=3"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result, err := drParseStackResult(tc.output, 8192)
			if tc.valid {
				if err != nil {
					t.Fatalf("valid record rejected: %v", err)
				}
				if result.depth != 8192 {
					t.Fatalf("valid record depth=%d, want 8192", result.depth)
				}
				return
			}
			if err == nil {
				t.Fatalf("malformed record accepted: %+v", result)
			}
		})
	}
}

func TestDRMutantResultParserRejectsMalformedRecords(t *testing.T) {
	t.Parallel()
	const valid = "DR_STACK_MUTANT frame=small depth=8192 stack_growth=524288"
	tests := []struct {
		name   string
		output string
		valid  bool
	}{
		{name: "valid", output: valid, valid: true},
		{name: "missing frame", output: "DR_STACK_MUTANT depth=8192 stack_growth=524288"},
		{name: "missing depth", output: "DR_STACK_MUTANT frame=small stack_growth=524288"},
		{name: "missing stack growth", output: "DR_STACK_MUTANT frame=small depth=8192"},
		{name: "negative growth", output: "DR_STACK_MUTANT frame=small depth=8192 stack_growth=-1"},
		{name: "unknown field", output: "DR_STACK_MUTANT frame=small depth=8192 stack_growth=524288 extra=1"},
		{name: "duplicate field", output: "DR_STACK_MUTANT frame=small depth=8192 depth=8192 stack_growth=524288"},
		{name: "duplicate record", output: valid + "\n" + valid},
		{name: "truncated diagnostic", output: valid + "\nDR_STACK_DIAGNOSTIC output_truncated"},
		{name: "unknown record", output: valid + "\nDR_STACK_OTHER frame=small"},
		{name: "mismatched frame", output: "DR_STACK_MUTANT frame=large depth=8192 stack_growth=524288"},
		{name: "mismatched depth", output: "DR_STACK_MUTANT frame=small depth=3200 stack_growth=524288"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			growth, err := drParseMutantStackResult(tc.output, drStackChildMutantSmall, 8192)
			growthValue := growth.stackGrowth
			if tc.valid {
				if err != nil {
					t.Fatalf("valid record rejected: %v", err)
				}
				if growthValue != 524288 {
					t.Fatalf("valid record growth=%d, want 524288", growthValue)
				}
				return
			}
			if err == nil {
				t.Fatalf("malformed record accepted: growth=%d", growthValue)
			}
		})
	}
}

func TestDRMeasurementControlsDisableAndRestoreMemoryLimit(t *testing.T) {
	// This test deliberately leaves the process-wide limit in a constrained
	// state while the helper sets up its measurement. The callback runs in the
	// measured goroutine, so it proves the limit is disabled at the exact point
	// where StackInuse and NumGC are sampled.
	previousLimit := debug.SetMemoryLimit(64 << 10)
	defer debug.SetMemoryLimit(previousLimit)
	var observedLimit int64
	drMeasureRateWithRuntimeControls(t, func() (economics.Valuation, error) {
		return economics.Valuation{}, nil
	}, func() {
		observedLimit = debug.SetMemoryLimit(-1)
	})
	if observedLimit != math.MaxInt64 {
		t.Fatalf("measurement memory limit=%d, want disabled limit %d", observedLimit, int64(math.MaxInt64))
	}
	if restored := debug.SetMemoryLimit(-1); restored != 64<<10 {
		t.Fatalf("memory limit after measurement=%d, want prior limit %d", restored, int64(64<<10))
	}
}

func TestDRMeasurementRejectsExplicitGC(t *testing.T) {
	if mode := drStackChildMode(); mode != "" {
		drRunStackChild(t, mode)
		return
	}
	output, err := drRunStackChildProcess(t, drStackChildExplicitGC, "^TestDRMeasurementRejectsExplicitGC$")
	if err == nil {
		t.Fatalf("explicit-GC measurement child unexpectedly passed: %s", output)
	}
	if !strings.Contains(output, "DR_STACK_DIAGNOSTIC gc_during_measurement") {
		t.Fatalf("explicit-GC measurement child failed without the bounded diagnostic: %s", output)
	}
}
