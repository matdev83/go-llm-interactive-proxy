package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// ClosePhase classifies when a generation-owned cleanup runs (design Resource Ledger).
type ClosePhase uint8

const (
	// PhasePrepare cleanups run on rollback after fallible prepare work.
	PhasePrepare ClosePhase = iota + 1
	// PhaseActivate cleanups run on rollback after commit-safe activate work.
	PhaseActivate
	// PhaseQuiesce stops admission-independent generation workers after retirement.
	PhaseQuiesce
	// PhaseClose releases clients, backend handles, and idle transports after drain.
	PhaseClose
)

// ResourceLedger tracks candidate/generation-owned resources for reverse-order
// rollback, quiesce, and close. It never owns ProcessServices.
type ResourceLedger struct {
	mu      sync.Mutex
	entries []*ledgerEntry

	rollbackOnce sync.Once
	quiesceOnce  sync.Once
	closeOnce    sync.Once
	prepareOnce  sync.Once
	activateOnce sync.Once

	rollbackErr error
	quiesceErr  error
	closeErr    error
	prepareErr  error
	activateErr error

	prepared atomic.Bool
	closed   atomic.Bool
}

type ledgerEntry struct {
	name  string
	phase ClosePhase
	start func(context.Context) error
	stop  func(context.Context) error

	stopOnce       sync.Once
	stopErr        error
	startAttempted atomic.Bool
	started        atomic.Bool
}

// NewResourceLedger returns an empty candidate-owned ledger.
func NewResourceLedger() *ResourceLedger {
	return &ResourceLedger{}
}

// Len returns the number of registered entries.
func (l *ResourceLedger) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// Add registers a context-aware cleanup for phase (design ResourceLedger.Add).
func (l *ResourceLedger) Add(name string, phase ClosePhase, closeFn func(context.Context) error) {
	if l == nil || closeFn == nil {
		return
	}
	l.AddAction(name, phase, nil, closeFn)
}

// AddClose registers a no-arg closer and returns an idempotent sync.Once wrapper
// suitable for legacy []func() error bags (keeps ownership append sites valid).
func (l *ResourceLedger) AddClose(name string, phase ClosePhase, closeFn func() error) func() error {
	if l == nil || closeFn == nil {
		return nil
	}
	entry := l.addEntry(name, phase, nil, func(context.Context) error { return closeFn() })
	return entry.syncStop
}

// AddAction registers optional start/stop hooks for a lifecycle phase.
func (l *ResourceLedger) AddAction(name string, phase ClosePhase, start, stop func(context.Context) error) {
	if l == nil {
		return
	}
	if start == nil && stop == nil {
		return
	}
	l.addEntry(name, phase, start, stop)
}

func (l *ResourceLedger) addEntry(name string, phase ClosePhase, start, stop func(context.Context) error) *ledgerEntry {
	l.mu.Lock()
	e := &ledgerEntry{name: name, phase: phase, start: start, stop: stop}
	if !l.closed.Load() {
		l.entries = append(l.entries, e)
		l.mu.Unlock()
		return e
	}
	l.mu.Unlock()

	// Accept-or-immediately-close: an acquired close-only resource racing
	// rollback/close must either join the ledger or run cleanup exactly once.
	// Run user cleanup outside l.mu so a closer may safely inspect the ledger.
	// A start-backed action registered after closure was never entered and must
	// not receive Stop.
	if stop != nil && start == nil {
		e.stopOnce.Do(func() {
			e.stopErr = stop(context.Background())
		})
	}
	return e
}

func (e *ledgerEntry) syncStop() error {
	if e == nil {
		return nil
	}
	e.stopOnce.Do(func() {
		if e.stop == nil {
			return
		}
		e.stopErr = e.stop(context.Background())
	})
	return e.stopErr
}

func (e *ledgerEntry) stopCtx(ctx context.Context) error {
	if e == nil {
		return nil
	}
	e.stopOnce.Do(func() {
		if e.stop == nil {
			return
		}
		if ctx == nil {
			ctx = context.Background()
		}
		e.stopErr = e.stop(ctx)
	})
	return e.stopErr
}

// Prepare runs PhasePrepare start hooks in acquisition order. Failure does not
// auto-rollback; callers must Rollback.
func (l *ResourceLedger) Prepare(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.prepareOnce.Do(func() {
		l.prepareErr = l.runStarts(ctx, PhasePrepare)
		if l.prepareErr == nil {
			l.prepared.Store(true)
		}
	})
	return l.prepareErr
}

// Activate runs PhaseActivate start hooks. Activate is commit-safe and bounded;
// errors fail preparation before publication.
func (l *ResourceLedger) Activate(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.activateOnce.Do(func() {
		l.activateErr = l.runStarts(ctx, PhaseActivate)
	})
	return l.activateErr
}

func (l *ResourceLedger) runStarts(ctx context.Context, phase ClosePhase) error {
	l.mu.Lock()
	entries := append([]*ledgerEntry(nil), l.entries...)
	l.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	for _, e := range entries {
		if e.phase != phase || e.start == nil {
			continue
		}
		// Mark attempted before invoking start so partial/failed starts still
		// receive conservative Stop on rollback; later never-attempted entries skip.
		e.startAttempted.Store(true)
		if err := e.start(ctx); err != nil {
			return fmt.Errorf("runtimebundle: ledger prepare %q: %w", e.name, err)
		}
		e.started.Store(true)
	}
	return nil
}

// Rollback closes all registered entries in reverse acquisition order (req 3.4).
func (l *ResourceLedger) Rollback(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.rollbackOnce.Do(func() {
		l.closed.Store(true)
		l.rollbackErr = l.stopReverse(ctx, nil)
	})
	return l.rollbackErr
}

// Quiesce runs PhaseQuiesce cleanups once in reverse order (req 10.5).
func (l *ResourceLedger) Quiesce(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.quiesceOnce.Do(func() {
		l.quiesceErr = l.stopReverse(ctx, func(e *ledgerEntry) bool {
			return e.phase == PhaseQuiesce
		})
	})
	return l.quiesceErr
}

// Close runs remaining PhaseClose/Prepare/Activate cleanups once in reverse order
// after drain (req 10.6). Quiesce entries are skipped if already quiesced.
func (l *ResourceLedger) Close(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		l.closed.Store(true)
		l.closeErr = l.stopReverse(ctx, func(e *ledgerEntry) bool {
			return e.phase != PhaseQuiesce
		})
	})
	return l.closeErr
}

func (l *ResourceLedger) stopReverse(ctx context.Context, match func(*ledgerEntry) bool) error {
	l.mu.Lock()
	entries := append([]*ledgerEntry(nil), l.entries...)
	l.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	var out error
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if match != nil && !match(e) {
			continue
		}
		// Start-backed entries that were never attempted must not receive Stop.
		// Close-only acquired resources (no start) always close. Attempted starts
		// (success or partial failure) get conservative Stop cleanup.
		if e.start != nil && !e.startAttempted.Load() {
			continue
		}
		if err := safeLedgerStop(ctx, e); err != nil {
			out = errors.Join(out, fmt.Errorf("runtimebundle: ledger close %q: %w", e.name, err))
		}
	}
	return out
}

func safeLedgerStop(ctx context.Context, e *ledgerEntry) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("runtimebundle: ledger close panic: %v", recovered)
		}
	}()
	return e.stopCtx(ctx)
}

// LegacyClosers returns once-wrapped no-arg closers in acquisition order for
// Built.Closers compatibility. Prefer Rollback/Quiesce/Close on the ledger.
func (l *ResourceLedger) LegacyClosers() []func() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]func() error, 0, len(l.entries))
	for _, e := range l.entries {
		entry := e
		out = append(out, entry.syncStop)
	}
	return out
}
