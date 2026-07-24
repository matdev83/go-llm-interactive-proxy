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

// retireAdmission is a context-aware per-generation retirement admission gate.
// It replaces a plain mutex so a background retirement blocked on a pin cannot
// make a context-bounded caller (e.g. ShutdownDetached) block forever: a
// waiter gives up as soon as its own context is done instead of waiting on an
// unbounded Lock(). No polling/sleeps are used.
type retireAdmission struct {
	mu   sync.Mutex
	held bool
	wait chan struct{}
}

func newRetireAdmission() retireAdmission {
	return retireAdmission{wait: make(chan struct{})}
}

// acquire blocks until this generation's retirement admission is free or ctx
// is done, whichever happens first.
func (a *retireAdmission) acquire(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		a.mu.Lock()
		if !a.held {
			a.held = true
			a.mu.Unlock()
			return nil
		}
		wait := a.wait
		a.mu.Unlock()
		select {
		case <-wait:
			continue
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// release frees admission and wakes any waiters. Safe to call once per
// successful acquire.
func (a *retireAdmission) release() {
	a.mu.Lock()
	a.held = false
	woken := a.wait
	a.wait = make(chan struct{})
	a.mu.Unlock()
	close(woken)
}

// retireGeneration quiesces once, waits for drain, then closes with bounded
// cleanup retries. Quiesce/cleanup errors and panics are isolated without
// altering the active generation. Idempotent: a second call on an
// already-closed generation returns ErrAlreadyClosed. Concurrent calls for
// different generations do not block each other; concurrent calls for the
// *same* generation are serialized by that generation's own context-aware
// retirement admission (no process-wide lock or worker map).
//
// The QuiesceCloser is derived solely from the Generation: its bound
// RequestPlane when set, otherwise its owned payload when it implements
// QuiesceCloser. Callers cannot substitute an unrelated collaborator.
//
// When retirement drained with zero refs before this ran (no quiesce window),
// it still invokes Quiesce once before BeginClose so generation workers stop.
func retireGeneration(ctx context.Context, g *Generation, policy CleanupPolicy, observer *ReloadObserver) (RetirementStatus, error) {
	if g == nil {
		return RetirementStatus{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := g.retireAdmit.acquire(ctx); err != nil {
		return RetirementStatus{GenerationID: g.ID()}, err
	}
	defer g.retireAdmit.release()

	status := RetirementStatus{GenerationID: g.ID()}
	st := g.Lifecycle()
	if st == GenClosed {
		return status, ErrAlreadyClosed
	}

	owned := generationQuiesceCloser(g)
	var out error

	switch st {
	case GenRetiring:
		if err := g.BeginQuiesce(); err != nil {
			return status, errors.Join(out, err)
		}
		fallthrough
	case GenQuiescing:
		out = errors.Join(out, runQuiesce(ctx, owned, observer, &status))
		if err := g.MarkQuiesced(); err != nil {
			out = errors.Join(out, err)
		}
	case GenDrained:
		// Zero-ref fast path skipped quiescing; still stop workers before close.
		out = errors.Join(out, runQuiesce(ctx, owned, observer, &status))
	case GenQuiesced, GenClosing:
		// resume without re-entering Quiesce
	default:
		return status, ErrIllegalTransition
	}

	select {
	case <-g.Drained():
	case <-ctx.Done():
		return status, errors.Join(out, ctx.Err())
	}

	if g.Lifecycle() == GenDrained {
		if err := g.BeginClose(); err != nil {
			return status, errors.Join(out, err)
		}
	}
	if g.Lifecycle() == GenClosing {
		out = errors.Join(out, runCleanup(ctx, g, policy, observer, &status))
	}
	return status, out
}

// generationQuiesceCloser derives the authoritative QuiesceCloser from the
// generation alone: the bound RequestPlane is authoritative when set, else
// the owned payload when it satisfies QuiesceCloser.
func generationQuiesceCloser(g *Generation) QuiesceCloser {
	if plane := g.RequestPlane(); plane != nil {
		return plane
	}
	g.payloadMu.Lock()
	owned := g.owned
	g.payloadMu.Unlock()
	if qc, ok := owned.(QuiesceCloser); ok {
		return qc
	}
	return nil
}

func runQuiesce(ctx context.Context, owned QuiesceCloser, observer *ReloadObserver, status *RetirementStatus) error {
	if owned == nil {
		return nil
	}
	start := time.Now()
	if err := safeQuiesce(ctx, owned); err != nil {
		status.Outcome = LifecycleOutcomeQuiesceFailed
		status.Err = err
		if observer != nil {
			observer.ObserveLifecycle(ctx, "quiesce", string(LifecycleOutcomeQuiesceFailed), time.Since(start))
		}
		return err
	}
	if observer != nil {
		observer.ObserveLifecycle(ctx, "quiesce", "ok", time.Since(start))
	}
	return nil
}

func runCleanup(ctx context.Context, g *Generation, policy CleanupPolicy, observer *ReloadObserver, status *RetirementStatus) error {
	maxAttempts := policy.maxAttempts()
	var closeErr error
	start := time.Now()
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
		status.Outcome = LifecycleOutcomeCleanupFailed
		status.Err = closeErr
		if observer != nil {
			observer.ObserveLifecycle(ctx, "cleanup", string(LifecycleOutcomeCleanupFailed), time.Since(start))
		}
		return closeErr
	}
	if status.Outcome == "" {
		status.Outcome = LifecycleOutcomeOK
		if observer != nil {
			observer.ObserveLifecycle(ctx, "cleanup", "ok", time.Since(start))
		}
	}
	return nil
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
