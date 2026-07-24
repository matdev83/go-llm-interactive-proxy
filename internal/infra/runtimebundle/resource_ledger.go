package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
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

// ledgerLifeState is the canonical generation-resource phase state machine.
type ledgerLifeState uint8

const (
	ledgerLifeOpen ledgerLifeState = iota
	ledgerLifeQuiesced
	ledgerLifeClosed
	ledgerLifeRolledBack
)

// ResourceLedger tracks candidate/generation-owned resources for reverse-order
// rollback, quiesce, and close. It is the sole generation-resource phase owner:
// prepare/activate idempotency, candidate rollback, generation quiesce, and
// retryable generation close all live here. It never owns ProcessServices.
type ResourceLedger struct {
	mu      sync.Mutex
	cond    *sync.Cond
	entries []*ledgerEntry

	state ledgerLifeState

	preparing   bool
	activating  bool
	quiescing   bool
	rollingBack bool
	closing     bool

	prepareDone  bool
	activateDone bool
	quiesceDone  bool

	prepareErr  error
	activateErr error
	quiesceErr  error
	rollbackErr error
	closeErr    error

	// sealed rejects further joins into the entry list; late acquisitions are
	// cleaned immediately outside mu (accept-or-immediately-close).
	sealed bool

	prepared atomic.Bool
}

type ledgerEntry struct {
	name  string
	phase ClosePhase
	start func(context.Context) error
	stop  func(context.Context) error

	mu              sync.Mutex
	cond            *sync.Cond
	cleaning        bool
	cleanedOK       bool
	terminalClaimed bool
	cleanErr        error

	startAttempted atomic.Bool
	started        atomic.Bool
}

// NewResourceLedger returns an empty candidate-owned ledger.
func NewResourceLedger() *ResourceLedger {
	l := &ResourceLedger{}
	l.cond = sync.NewCond(&l.mu)
	return l
}

func (l *ResourceLedger) ensureCond() {
	if l.cond == nil {
		l.cond = sync.NewCond(&l.mu)
	}
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

// AddClose registers a no-arg closer and returns an idempotent terminal syncStop
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
	e := &ledgerEntry{name: name, phase: phase, start: start, stop: stop}
	e.cond = sync.NewCond(&e.mu)

	l.mu.Lock()
	l.ensureCond()
	immediate := false
	switch {
	case l.sealed:
		immediate = true
	case (l.quiesceDone || l.quiescing || l.state == ledgerLifeQuiesced ||
		l.state == ledgerLifeClosed || l.state == ledgerLifeRolledBack) &&
		phase == PhaseQuiesce:
		immediate = true
	default:
		l.entries = append(l.entries, e)
		l.mu.Unlock()
		return e
	}
	l.mu.Unlock()

	// Accept-or-immediately-close: an acquired close-only resource racing
	// rollback/close/quiesce must either join the ledger or run cleanup exactly
	// once. Run user cleanup outside l.mu so a closer may safely inspect the ledger.
	// A start-backed action registered after closure was never entered and must
	// not receive Stop.
	if immediate && stop != nil && start == nil {
		_ = e.claimAndStop(context.Background(), true)
	}
	return e
}

func (e *ledgerEntry) syncStop() error {
	if e == nil {
		return nil
	}
	return e.claimAndStop(context.Background(), true)
}

// claimAndStop runs stop at most once on success. terminal=true permanently
// claims the entry even on failure (rollback/late/syncStop). terminal=false
// leaves a failed/panicking entry eligible for a later Close retry.
func (e *ledgerEntry) claimAndStop(ctx context.Context, terminal bool) error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	if e.cond == nil {
		e.cond = sync.NewCond(&e.mu)
	}
	for e.cleaning {
		e.cond.Wait()
	}
	if e.cleanedOK {
		e.mu.Unlock()
		return nil
	}
	if e.terminalClaimed {
		err := e.cleanErr
		e.mu.Unlock()
		return err
	}
	e.cleaning = true
	e.mu.Unlock()

	err := safeLedgerStop(ctx, e)

	e.mu.Lock()
	e.cleaning = false
	if err == nil {
		e.cleanedOK = true
		e.cleanErr = nil
	} else if terminal {
		e.terminalClaimed = true
		e.cleanErr = err
	} else {
		e.cleanErr = err
	}
	e.cond.Broadcast()
	e.mu.Unlock()
	return err
}

// Prepare runs PhasePrepare start hooks in acquisition order. Failure does not
// auto-rollback; callers must Rollback. After quiesce/rollback/close sealing,
// Prepare does not start resources and returns the stable terminal/quiesce
// outcome (or a prior prepare result when prepare already completed).
func (l *ResourceLedger) Prepare(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	l.ensureCond()
	for l.preparing || l.activating || l.quiescing || l.rollingBack || l.closing {
		l.cond.Wait()
	}
	if l.prepareDone {
		err := l.prepareErr
		l.mu.Unlock()
		return err
	}
	if blocked, err := l.startBlockedLocked(); blocked {
		l.mu.Unlock()
		return err
	}
	l.preparing = true
	l.mu.Unlock()

	err := l.runStarts(ctx, PhasePrepare)

	l.mu.Lock()
	l.prepareDone = true
	l.prepareErr = err
	if err == nil {
		l.prepared.Store(true)
	}
	l.preparing = false
	l.cond.Broadcast()
	l.mu.Unlock()
	return err
}

// Activate runs PhaseActivate start hooks. Activate is commit-safe and bounded;
// errors fail preparation before publication. After quiesce/rollback/close
// sealing, Activate does not start resources and returns the stable
// terminal/quiesce outcome (or a prior activate result when already completed).
func (l *ResourceLedger) Activate(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	l.ensureCond()
	for l.preparing || l.activating || l.quiescing || l.rollingBack || l.closing {
		l.cond.Wait()
	}
	if l.activateDone {
		err := l.activateErr
		l.mu.Unlock()
		return err
	}
	if blocked, err := l.startBlockedLocked(); blocked {
		l.mu.Unlock()
		return err
	}
	l.activating = true
	l.mu.Unlock()

	err := l.runStarts(ctx, PhaseActivate)

	l.mu.Lock()
	l.activateDone = true
	l.activateErr = err
	l.activating = false
	l.cond.Broadcast()
	l.mu.Unlock()
	return err
}

// startBlockedLocked reports whether prepare/activate must refuse new starts.
// Caller holds l.mu. Terminal rollback/close and quiesce (retirement begun)
// permanently block starts; sealed close/rollback attempts that have not yet
// reached a terminal state also block.
func (l *ResourceLedger) startBlockedLocked() (bool, error) {
	switch l.state {
	case ledgerLifeClosed:
		return true, l.closeErr
	case ledgerLifeRolledBack:
		return true, l.rollbackErr
	case ledgerLifeQuiesced:
		return true, l.quiesceErr
	}
	if l.quiesceDone {
		return true, l.quiesceErr
	}
	if l.sealed {
		if l.closeErr != nil {
			return true, l.closeErr
		}
		return true, l.rollbackErr
	}
	return false, nil
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

// Rollback closes all registered entries in reverse acquisition order and caches
// the terminal result (req 3.4). Failed entries are claimed exactly once.
func (l *ResourceLedger) Rollback(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	l.ensureCond()
	for l.preparing || l.activating || l.quiescing || l.rollingBack || l.closing {
		l.cond.Wait()
	}
	switch l.state {
	case ledgerLifeRolledBack:
		err := l.rollbackErr
		l.mu.Unlock()
		return err
	case ledgerLifeClosed:
		err := l.closeErr
		l.mu.Unlock()
		return err
	}
	l.rollingBack = true
	l.sealed = true
	l.mu.Unlock()

	err := l.stopReverse(ctx, nil, true)

	l.mu.Lock()
	l.rollbackErr = err
	l.state = ledgerLifeRolledBack
	l.quiesceDone = true
	l.rollingBack = false
	l.cond.Broadcast()
	l.mu.Unlock()
	return err
}

// Quiesce runs PhaseQuiesce cleanups at most once in reverse order (req 10.5).
// Concurrent callers share the in-flight attempt.
func (l *ResourceLedger) Quiesce(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	l.ensureCond()
	for l.preparing || l.activating || l.rollingBack || l.closing {
		l.cond.Wait()
	}
	switch l.state {
	case ledgerLifeClosed:
		err := l.closeErr
		l.mu.Unlock()
		return err
	case ledgerLifeRolledBack:
		err := l.rollbackErr
		l.mu.Unlock()
		return err
	}
	if l.quiescing {
		for l.quiescing {
			l.cond.Wait()
		}
		err := l.quiesceErr
		if l.state == ledgerLifeClosed {
			err = l.closeErr
		} else if l.state == ledgerLifeRolledBack {
			err = l.rollbackErr
		}
		l.mu.Unlock()
		return err
	}
	if l.quiesceDone || l.state == ledgerLifeQuiesced {
		err := l.quiesceErr
		l.mu.Unlock()
		return err
	}
	l.quiescing = true
	l.mu.Unlock()

	err := l.stopReverse(ctx, func(e *ledgerEntry) bool {
		return e.phase == PhaseQuiesce
	}, true)

	l.mu.Lock()
	l.quiesceErr = err
	l.quiesceDone = true
	if l.state == ledgerLifeOpen {
		l.state = ledgerLifeQuiesced
	}
	l.quiescing = false
	l.cond.Broadcast()
	l.mu.Unlock()
	return err
}

// Close runs generation resource cleanup. After successful Quiesce it executes
// remaining phases in reverse order and is retryable on cleanup failure/panic.
// Before Quiesce (unpublished GenerationRuntime/CandidateRuntime) it performs
// full rollback including PhaseQuiesce, also retryable on failure. Concurrent
// callers share the in-flight attempt; a later explicit Close after failure
// retries only entries that did not clean successfully.
func (l *ResourceLedger) Close(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	l.ensureCond()
	for l.preparing || l.activating || l.quiescing || l.rollingBack {
		l.cond.Wait()
	}
	switch l.state {
	case ledgerLifeClosed:
		err := l.closeErr
		l.mu.Unlock()
		return err
	case ledgerLifeRolledBack:
		err := l.rollbackErr
		l.mu.Unlock()
		return err
	}
	if l.closing {
		for l.closing {
			l.cond.Wait()
		}
		err := l.closeErr
		if l.state == ledgerLifeClosed {
			err = l.closeErr
		} else if l.state == ledgerLifeRolledBack {
			err = l.rollbackErr
		}
		l.mu.Unlock()
		return err
	}
	wasQuiesced := l.state == ledgerLifeQuiesced || l.quiesceDone
	l.closing = true
	l.sealed = true
	l.mu.Unlock()

	var err error
	if wasQuiesced {
		err = l.stopReverse(ctx, func(e *ledgerEntry) bool {
			return e.phase != PhaseQuiesce
		}, false)
	} else {
		err = l.stopReverse(ctx, nil, false)
	}

	l.mu.Lock()
	l.closeErr = err
	if err == nil {
		l.state = ledgerLifeClosed
		l.quiesceDone = true
	}
	l.closing = false
	l.cond.Broadcast()
	l.mu.Unlock()
	return err
}

func (l *ResourceLedger) stopReverse(ctx context.Context, match func(*ledgerEntry) bool, terminal bool) error {
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
		if err := e.claimAndStop(ctx, terminal); err != nil {
			out = errors.Join(out, fmt.Errorf("runtimebundle: ledger close %q: %w", e.name, err))
		}
	}
	return out
}

func safeLedgerStop(ctx context.Context, e *ledgerEntry) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("runtimebundle: ledger close panic: %s", configreload.SanitizePanicValue(recovered))
		}
	}()
	if e == nil || e.stop == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return e.stop(ctx)
}
