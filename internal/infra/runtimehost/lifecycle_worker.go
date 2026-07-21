package runtimehost

import (
	"context"
	"errors"
	"sync"
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
type LifecycleWorker struct {
	policy CleanupPolicy

	statusMu sync.Mutex
	last     RetirementStatus
}

// NewLifecycleWorker returns a process-owned retirement worker with default cleanup policy.
func NewLifecycleWorker() *LifecycleWorker {
	return NewLifecycleWorkerWithPolicy(CleanupPolicy{})
}

// NewLifecycleWorkerWithPolicy returns a retirement worker with an explicit cleanup retry budget.
func NewLifecycleWorkerWithPolicy(policy CleanupPolicy) *LifecycleWorker {
	return &LifecycleWorker{policy: policy}
}

// LastStatus returns a copy of the most recent retirement status snapshot.
func (w *LifecycleWorker) LastStatus() RetirementStatus {
	if w == nil {
		return RetirementStatus{}
	}
	w.statusMu.Lock()
	defer w.statusMu.Unlock()
	return w.last
}

func (w *LifecycleWorker) setStatus(st RetirementStatus) {
	if w == nil {
		return
	}
	w.statusMu.Lock()
	w.last = st
	w.statusMu.Unlock()
}

// Retire quiesces once, waits for drain, then closes with bounded cleanup retries.
// Quiesce/cleanup errors and panics are isolated without altering the active generation.
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
	// A bound publication plane is the authoritative generation owner. Ignore a
	// stale/mismatched compatibility argument so the served bundle is always the
	// bundle that gets quiesced; final Close is likewise owned by Generation.
	if plane := g.RequestPlane(); plane != nil {
		owned = plane
	}

	g.retireMu.Lock()
	defer g.retireMu.Unlock()

	st := g.Lifecycle()
	if st == GenClosed || g.closed.Load() {
		return ErrAlreadyClosed
	}

	status := RetirementStatus{GenerationID: g.ID()}
	var out error
	quiesced := false

	switch st {
	case GenRetiring:
		if err := g.BeginQuiesce(); err != nil {
			w.setStatus(status)
			return errors.Join(out, err)
		}
		st = GenQuiescing
		fallthrough
	case GenQuiescing:
		if owned != nil {
			if err := safeQuiesce(ctx, owned); err != nil {
				out = errors.Join(out, err)
				status.Outcome = LifecycleOutcomeQuiesceFailed
				status.Err = err
			}
		}
		quiesced = true
		if err := g.MarkQuiesced(); err != nil {
			out = errors.Join(out, err)
		}
	case GenDrained:
		// Zero-ref fast path skipped quiescing; still stop workers before close.
		if owned != nil {
			if err := safeQuiesce(ctx, owned); err != nil {
				out = errors.Join(out, err)
				status.Outcome = LifecycleOutcomeQuiesceFailed
				status.Err = err
			}
		}
		quiesced = true
	case GenQuiesced, GenClosing:
		// resume
	default:
		w.setStatus(status)
		return ErrIllegalTransition
	}

	if !quiesced && (st == GenQuiesced || g.Lifecycle() == GenQuiesced) {
		// already quiesced by a prior attempt
	}

	select {
	case <-g.Drained():
	case <-ctx.Done():
		w.setStatus(status)
		return errors.Join(out, ctx.Err())
	}

	if g.Lifecycle() == GenDrained {
		if err := g.BeginClose(); err != nil {
			w.setStatus(status)
			return errors.Join(out, err)
		}
	}
	if g.Lifecycle() == GenClosing {
		maxAttempts := w.policy.maxAttempts()
		var closeErr error
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			status.Attempts = attempt
			closeErr = safeClose(g)
			if closeErr == nil || errors.Is(closeErr, ErrAlreadyClosed) {
				closeErr = nil
				break
			}
			if g.Lifecycle() == GenClosed {
				closeErr = nil
				break
			}
		}
		if closeErr != nil {
			out = errors.Join(out, closeErr)
			status.Outcome = LifecycleOutcomeCleanupFailed
			status.Err = closeErr
		} else if status.Outcome == "" {
			status.Outcome = LifecycleOutcomeOK
		}
	}
	w.setStatus(status)
	return out
}

func safeQuiesce(ctx context.Context, owned QuiesceCloser) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError("quiesce", recovered)
		}
	}()
	return owned.Quiesce(ctx)
}

func safeClose(g *Generation) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError("cleanup", recovered)
		}
	}()
	return g.Close()
}
