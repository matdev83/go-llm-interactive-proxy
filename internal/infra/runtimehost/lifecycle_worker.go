package runtimehost

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
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
	policy   CleanupPolicy
	observer *ReloadObserver

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

// SetObserver attaches optional reload lifecycle telemetry (quiesce/cleanup spans).
func (w *LifecycleWorker) SetObserver(obs *ReloadObserver) {
	if w == nil {
		return
	}
	w.observer = obs
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

	switch st {
	case GenRetiring:
		if err := g.BeginQuiesce(); err != nil {
			w.setStatus(status)
			return errors.Join(out, err)
		}
		fallthrough
	case GenQuiescing:
		if owned != nil {
			qStart := time.Now()
			if err := safeQuiesce(ctx, owned); err != nil {
				out = errors.Join(out, err)
				status.Outcome = LifecycleOutcomeQuiesceFailed
				status.Err = err
				if w.observer != nil {
					w.observer.ObserveLifecycle(ctx, "quiesce", string(LifecycleOutcomeQuiesceFailed), time.Since(qStart))
				}
			} else if w.observer != nil {
				w.observer.ObserveLifecycle(ctx, "quiesce", "ok", time.Since(qStart))
			}
		}
		if err := g.MarkQuiesced(); err != nil {
			out = errors.Join(out, err)
		}
	case GenDrained:
		// Zero-ref fast path skipped quiescing; still stop workers before close.
		if owned != nil {
			qStart := time.Now()
			if err := safeQuiesce(ctx, owned); err != nil {
				out = errors.Join(out, err)
				status.Outcome = LifecycleOutcomeQuiesceFailed
				status.Err = err
				if w.observer != nil {
					w.observer.ObserveLifecycle(ctx, "quiesce", string(LifecycleOutcomeQuiesceFailed), time.Since(qStart))
				}
			} else if w.observer != nil {
				w.observer.ObserveLifecycle(ctx, "quiesce", "ok", time.Since(qStart))
			}
		}
	case GenQuiesced, GenClosing:
		// resume
	default:
		w.setStatus(status)
		return ErrIllegalTransition
	}

	// GenQuiesced/GenClosing resume paths fall through here without re-entering Quiesce.

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
		cStart := time.Now()
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
			if w.observer != nil {
				w.observer.ObserveLifecycle(ctx, "cleanup", string(LifecycleOutcomeCleanupFailed), time.Since(cStart))
			}
		} else if status.Outcome == "" {
			status.Outcome = LifecycleOutcomeOK
			if w.observer != nil {
				w.observer.ObserveLifecycle(ctx, "cleanup", "ok", time.Since(cStart))
			}
		}
	}
	w.setStatus(status)
	return out
}

func safeQuiesce(ctx context.Context, owned QuiesceCloser) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError("quiesce", configreload.SanitizePanicValue(recovered))
		}
	}()
	return owned.Quiesce(ctx)
}

func safeClose(g *Generation) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError("cleanup", configreload.SanitizePanicValue(recovered))
		}
	}()
	return g.Close()
}
