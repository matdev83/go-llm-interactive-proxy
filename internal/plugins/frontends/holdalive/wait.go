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

// Wait runs fn and, while it is pending, optionally emits HTTP 102 Processing informational
// responses. 1xx responses do not commit the final status, so normal JSON/SSE error contracts remain intact.
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
			<-done
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
