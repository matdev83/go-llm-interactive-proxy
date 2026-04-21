package stream_test

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
)

// Recv returning early due to caller context cancellation must unblock the inner
// Recv without requiring Close(), otherwise the background reader goroutine leaks
// for every abandoned wait.
func TestKeepalive_recvDeadlineWithoutClose_doesNotLeakReaderGoroutines(t *testing.T) {
	t.Parallel()

	runtime.GC()
	baseline := runtime.NumGoroutine()

	const iters = 40
	for range iters {
		block := make(chan struct{})
		inner := &blockingRecvStream{unblock: block}

		ka := mustNewKeepalive(t, inner, stream.KeepaliveConfig{
			Interval: time.Hour,
		})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		_, err := ka.Recv(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Recv: got %v want DeadlineExceeded", err)
		}

		// Intentionally do not Close ka: caller context expiry must release the reader.
		_ = ka
	}

	for range 300 {
		runtime.GC()
		if n := runtime.NumGoroutine(); n <= baseline+2 {
			return
		}
		runtime.Gosched()
	}

	t.Fatalf("goroutine leak suspected after %d abandoned Recv waits: baseline=%d now=%d", iters, baseline, runtime.NumGoroutine())
}
