package runtimehost

import (
	"context"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

// attemptAdmissionKind classifies TryStart without a second state machine.
type attemptAdmissionKind int

const (
	// admissionAdmitted: lease armed; attempt observable as active.
	admissionAdmitted attemptAdmissionKind = iota
	// admissionBusyAPI: API while active — canonical busy, no queue (req 6.9).
	admissionBusyAPI
	// admissionPendingHUP: first SIGHUP while active sets at most one pending follow-up (req 6.8).
	admissionPendingHUP
	// admissionCoalescedHUP: additional SIGHUP increments coalesced; no extra queued work (req 6.8).
	admissionCoalescedHUP
	// admissionRejectedShutdown: shutting down; no lease (req 6.1).
	admissionRejectedShutdown
)

// attemptFinishKind classifies the atomic completion transition.
type attemptFinishKind int

const (
	// finishReleasedIdle: final active lease released; idle waiters wake.
	finishReleasedIdle attemptFinishKind = iota
	// finishFollowUpClaimed: claimed exactly one pending HUP, cleared/reset
	// its coalesced count, returned FollowUpLease; gate stays non-idle with no
	// admission window between release and follow-up arm.
	finishFollowUpClaimed
	// finishAlreadyCompleted: concurrent/sequential duplicate Complete —
	// panic-free no-op; advances/finishes at most once.
	finishAlreadyCompleted
)

// attemptAdmissionOutcome is the immutable TryStart result. Busy and coalesced
// metadata live on the value — no mutable snapshot/getter API for admission.
type attemptAdmissionOutcome struct {
	Kind             attemptAdmissionKind
	Lease            *attemptLease // non-nil only for admissionAdmitted
	CoalescedSignals int64         // safe count for pending/coalesced HUP busy results
	Category         sdkreload.ResultCategory
	ReasonCategory   string
}

// attemptFinishOutcome is the immutable Complete() result distinguishing final
// idle release from atomic pending-follow-up claim.
type attemptFinishOutcome struct {
	Kind             attemptFinishKind
	FollowUpLease    *attemptLease // non-nil only for finishFollowUpClaimed
	CoalescedSignals int64         // count claimed with the follow-up (gate resets after claim)
}

// attemptGateSnapshot is a narrow immutable projection of gate-owned status
// fields for Coordinator.Status (Busy/PendingSignal/CoalescedSignals).
type attemptGateSnapshot struct {
	Busy             bool
	PendingSignal    bool
	CoalescedSignals int64
}

// attemptGate exclusively owns reload busy admission, one pending SIGHUP
// reservation, coalesced signal count, active attempt context/cancellation/
// completion lease, shutdown admission rejection, and non-polling idle
// notification (req 6.1, 6.5-6.9).
type attemptGate struct {
	mu sync.Mutex

	shutdown   bool
	pendingHUP bool
	coalesced  int64
	active     *attemptLease
	nextToken  uint64

	// idleNotify is never nil. While idle it is a closed channel; while busy it
	// is an open channel closed exactly once on final release under mu.
	idleNotify chan struct{}
}

// attemptLease is one admitted attempt token. Cancellation is gate-owned;
// Complete and Abandon are the finish/advance transitions (Abandon never
// claims pending follow-up work).
type attemptLease struct {
	gate     *attemptGate
	token    uint64
	ctx      context.Context
	cancel   context.CancelFunc
	finished bool // under gate.mu
}

func newAttemptGate() *attemptGate {
	ch := make(chan struct{})
	close(ch) // start idle
	return &attemptGate{idleNotify: ch}
}

// TryStart admits one attempt or returns an immutable busy/reject outcome with
// no live lease. Admission of busy/active state and the completion lease become
// visible in one lock transition (req 6.5, 6.6).
func (g *attemptGate) TryStart(ctx context.Context, trigger sdkreload.Trigger) attemptAdmissionOutcome {
	if g == nil {
		return attemptAdmissionOutcome{
			Kind:           admissionRejectedShutdown,
			Category:       sdkreload.ResultCanceled,
			ReasonCategory: configreload.StageShutdown,
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.shutdown {
		return attemptAdmissionOutcome{
			Kind:           admissionRejectedShutdown,
			Category:       sdkreload.ResultCanceled,
			ReasonCategory: configreload.StageShutdown,
		}
	}

	if g.active != nil {
		if trigger.Kind == sdkreload.TriggerSIGHUP {
			if g.pendingHUP {
				g.coalesced++
				coal := g.coalesced
				return attemptAdmissionOutcome{
					Kind:             admissionCoalescedHUP,
					CoalescedSignals: coal,
					Category:         sdkreload.ResultBusy,
					ReasonCategory:   configreload.StageCoalesce,
				}
			}
			g.pendingHUP = true
			return attemptAdmissionOutcome{
				Kind:           admissionPendingHUP,
				Category:       sdkreload.ResultBusy,
				ReasonCategory: configreload.StageCoalesce,
			}
		}
		return attemptAdmissionOutcome{
			Kind:           admissionBusyAPI,
			Category:       sdkreload.ResultBusy,
			ReasonCategory: configreload.StageBusy,
		}
	}

	base := context.WithoutCancel(ctx)
	if base == nil {
		base = context.Background()
	}
	leaseCtx, cancel := context.WithCancel(base)
	g.nextToken++
	lease := &attemptLease{
		gate:   g,
		token:  g.nextToken,
		ctx:    leaseCtx,
		cancel: cancel,
	}
	notify := make(chan struct{})
	g.idleNotify = notify
	g.active = lease
	return attemptAdmissionOutcome{
		Kind:  admissionAdmitted,
		Lease: lease,
	}
}

// BeginShutdown atomically marks shutdown, clears pending/count, rejects future
// starts, and cancels any active lease context (req 6.1, 6.7). Final Complete
// still wakes idle waiters. Repeated/concurrent calls are safe.
func (g *attemptGate) BeginShutdown() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.shutdown = true
	g.pendingHUP = false
	g.coalesced = 0
	cancel := context.CancelFunc(nil)
	if g.active != nil {
		cancel = g.active.cancel
	}
	g.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// WaitForIdle waits on completion notifications until no attempt is active.
// It must not poll, sleep, or use periodic timers (req 6.5). Nil ctx is treated
// as background. Cancellation/deadline of ctx cancels only this wait.
func (g *attemptGate) WaitForIdle(ctx context.Context) error {
	if g == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		g.mu.Lock()
		idle := g.active == nil
		notify := g.idleNotify
		g.mu.Unlock()
		if idle {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-notify:
			// Recheck: a follow-up may have armed immediately without going idle.
		}
	}
}

// Snapshot returns a narrow immutable projection of Busy/PendingSignal/
// CoalescedSignals for Coordinator.Status.
func (g *attemptGate) Snapshot() attemptGateSnapshot {
	if g == nil {
		return attemptGateSnapshot{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return attemptGateSnapshot{
		Busy:             g.active != nil,
		PendingSignal:    g.pendingHUP,
		CoalescedSignals: g.coalesced,
	}
}

// shuttingDown reports whether BeginShutdown has been observed. Narrow query
// for Coordinator/runAttempt shutdown checks — not a broad getter surface.
func (g *attemptGate) shuttingDown() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.shutdown
}

// Context is the host-owned attempt context.
func (l *attemptLease) Context() context.Context {
	if l == nil || l.ctx == nil {
		return context.Background()
	}
	return l.ctx
}

// releaseActiveIdleLocked is the sole final-idle release transaction under
// g.mu: marks the lease finished, clears active/pending/coalesced, closes
// idleNotify exactly once, and returns cancel for invocation after unlock.
// Caller must hold g.mu and have verified l is the unfinished active token.
// Only Complete's final-idle path and Abandon may call this helper.
func (g *attemptGate) releaseActiveIdleLocked(l *attemptLease) context.CancelFunc {
	l.finished = true
	cancel := l.cancel
	notify := g.idleNotify
	g.active = nil
	g.pendingHUP = false
	g.coalesced = 0
	close(notify)
	return cancel
}

// Complete atomically finishes or advances the active lease exactly once.
func (l *attemptLease) Complete() attemptFinishOutcome {
	if l == nil || l.gate == nil {
		return attemptFinishOutcome{Kind: finishAlreadyCompleted}
	}
	g := l.gate
	g.mu.Lock()

	if l.finished || g.active != l {
		g.mu.Unlock()
		return attemptFinishOutcome{Kind: finishAlreadyCompleted}
	}

	if g.shutdown || !g.pendingHUP {
		cancel := g.releaseActiveIdleLocked(l)
		g.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return attemptFinishOutcome{Kind: finishReleasedIdle}
	}

	l.finished = true
	coal := g.coalesced
	g.pendingHUP = false
	g.coalesced = 0
	g.nextToken++
	follow := &attemptLease{
		gate:   g,
		token:  g.nextToken,
		ctx:    l.ctx,
		cancel: l.cancel,
	}
	g.active = follow
	g.mu.Unlock()
	return attemptFinishOutcome{
		Kind:             finishFollowUpClaimed,
		FollowUpLease:    follow,
		CoalescedSignals: coal,
	}
}

// Abandon releases this lease without claiming pending follow-up work.
// If this lease is still the active unfinished token, it atomically marks
// finished, discards pending HUP/coalesced work, clears active state, closes
// the idle notification exactly once, and cancels the host-owned context
// (req 6.7). Concurrent/sequential Abandon/Complete races are panic-free;
// only one transition wins. After Complete or a prior Abandon, Abandon is an
// idempotent no-op and never claims follow-up work.
func (l *attemptLease) Abandon() {
	if l == nil || l.gate == nil {
		return
	}
	g := l.gate
	g.mu.Lock()
	if l.finished || g.active != l {
		g.mu.Unlock()
		return
	}
	cancel := g.releaseActiveIdleLocked(l)
	g.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
