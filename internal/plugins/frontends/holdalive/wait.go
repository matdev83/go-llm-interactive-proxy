package holdalive

import (
	"context"
	"net/http"
	"time"
)

type Config struct {
	Enabled  bool
	Interval time.Duration
}

type result[T any] struct {
	value T
	err   error
}

// cancelDrainGrace bounds how long Wait waits for fn to finish after ctx is canceled.
// It is a var (not a const) only so in-package tests can shorten it; production code
// must not mutate it.
var cancelDrainGrace = 5 * time.Second

// Wait runs fn and, while it is pending, optionally emits HTTP 102 Processing informational
// responses. 1xx responses do not commit the final status, so normal JSON/SSE error contracts remain intact.
//
// Goroutine allowlist justification (audited exception to the "no per-request handler
// goroutines" rule): emitting 102 Processing keepalives requires the Execute call and
// the status write to proceed concurrently, and the caller's ctx must propagate into
// fn. The done channel is buffered (capacity 1) so the spawned goroutine always exits
// without a receiver, even when Wait returns early via the cancel path. The cancel
// path drains done so fn's terminal error cannot race a reused response writer, but
// the drain is bounded by cancelDrainGrace: a fn that ignores ctx cannot block Wait
// indefinitely — after the grace Wait returns ctx.Err() and the late send is absorbed
// by the buffered channel.
func Wait[T any](ctx context.Context, w http.ResponseWriter, cfg Config, fn func(context.Context) (T, error)) (T, error) {
	if !cfg.Enabled || cfg.Interval <= 0 || w == nil {
		return fn(ctx)
	}
	done := make(chan result[T], 1)
	go func() {
		v, err := fn(ctx)
		done <- result[T]{value: v, err: err}
	}()

	timer := time.NewTimer(cfg.Interval)
	defer timer.Stop()
	for {
		select {
		case r := <-done:
			return r.value, r.err
		case <-ctx.Done():
			var zero T
			select {
			case <-done:
			case <-time.After(cancelDrainGrace):
			}
			return zero, ctx.Err()
		case <-timer.C:
			w.WriteHeader(http.StatusProcessing)
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			timer.Reset(cfg.Interval)
		}
	}
}
