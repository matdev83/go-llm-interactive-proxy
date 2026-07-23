package lipruntime

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

// TestClose_RetryAfterOwnedCloserErrorExactlyOnceProcess characterizes public
// Runtime.Close retry after an owned closer fails mid-teardown: ProcessServices
// must close exactly once on the first successful process teardown, while a
// later tracing failure keeps Close retryable until tracing succeeds.
func TestClose_RetryAfterOwnedCloserErrorExactlyOnceProcess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := Build(ctx, Options{ConfigPath: testConfigPath(t), LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rt.host == nil || rt.host.Process == nil {
		t.Fatal("nil host/process")
	}

	var tracingCalls atomic.Int32
	rt.shutdownTracing = func(context.Context) error {
		n := tracingCalls.Add(1)
		if n == 1 {
			return errors.New("tracing shutdown boom")
		}
		return nil
	}

	err = rt.Close(ctx)
	if err == nil {
		t.Fatal("expected first Close to surface tracing failure")
	}
	if !strings.Contains(err.Error(), "tracing shutdown boom") {
		t.Fatalf("err=%v want tracing failure", err)
	}
	if !rt.host.Process.Closed() {
		t.Fatal("ProcessServices must close before tracing; first attempt must leave it closed")
	}
	if rt.closed {
		t.Fatal("facade must remain retryable after owned closer failure")
	}

	// Retry: ProcessServices.Close is exactly-once (closeOnce); tracing runs again.
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if tracingCalls.Load() != 2 {
		t.Fatalf("tracing calls=%d want 2 (retry after initial failure)", tracingCalls.Load())
	}
	if !rt.closed {
		t.Fatal("successful retry must mark facade closed")
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("idempotent Close after success: %v", err)
	}
	if tracingCalls.Load() != 2 {
		t.Fatalf("successful Close must not re-invoke tracing: calls=%d", tracingCalls.Load())
	}
}
