package runtimehost

import (
	"context"
	"errors"
)

// QuiesceCloser is generation-owned teardown with an explicit quiesce phase.
// OwnedCloser.Close runs after drain; Quiesce runs while retiring/quiescing.
type QuiesceCloser interface {
	OwnedCloser
	Quiesce(ctx context.Context) error
}

// LifecycleWorker owns post-commit quiesce → drain → close for retired generations.
// Publication never waits on this worker (req 5.9, 10.5-10.6, 13.5).
// Retirement serialization is per-generation (Generation.retireMu), so unrelated
// generations progress independently without a process-wide lock or worker map.
type LifecycleWorker struct{}

// NewLifecycleWorker returns a process-owned retirement worker.
func NewLifecycleWorker() *LifecycleWorker {
	return &LifecycleWorker{}
}

// Retire quiesces once, waits for drain, then closes once in ownership order.
// Quiesce/cleanup errors are returned without altering the active generation.
// Idempotent: a second call on an already-closed generation returns ErrAlreadyClosed.
// Concurrent Retire calls for different generations do not block each other.
//
// When retirement drained with zero refs before the worker ran (no quiesce window),
// Retire still invokes Quiesce once before BeginClose so generation workers stop.
func (w *LifecycleWorker) Retire(ctx context.Context, g *Generation, owned QuiesceCloser) error {
	if w == nil || g == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	g.retireMu.Lock()
	defer g.retireMu.Unlock()

	st := g.Lifecycle()
	if st == GenClosed || g.closed.Load() {
		return ErrAlreadyClosed
	}

	var out error
	quiesced := false

	switch st {
	case GenRetiring:
		if err := g.BeginQuiesce(); err != nil {
			return errors.Join(out, err)
		}
		st = GenQuiescing
		fallthrough
	case GenQuiescing:
		if owned != nil {
			if err := owned.Quiesce(ctx); err != nil {
				out = errors.Join(out, err)
			}
		}
		quiesced = true
		if err := g.MarkQuiesced(); err != nil {
			out = errors.Join(out, err)
		}
	case GenDrained:
		// Zero-ref fast path skipped quiescing; still stop workers before close.
		if owned != nil {
			if err := owned.Quiesce(ctx); err != nil {
				out = errors.Join(out, err)
			}
		}
		quiesced = true
	case GenQuiesced, GenClosing:
		// resume
	default:
		return ErrIllegalTransition
	}

	if !quiesced && (st == GenQuiesced || g.Lifecycle() == GenQuiesced) {
		// already quiesced by a prior attempt
	}

	<-g.Drained()

	if g.Lifecycle() == GenDrained {
		if err := g.BeginClose(); err != nil {
			return errors.Join(out, err)
		}
	}
	if g.Lifecycle() == GenClosing {
		if err := g.Close(); err != nil && !errors.Is(err, ErrAlreadyClosed) {
			out = errors.Join(out, err)
		}
	}
	return out
}
