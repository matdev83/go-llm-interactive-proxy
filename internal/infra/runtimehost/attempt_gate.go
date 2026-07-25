package runtimehost

import (
	"context"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

type attemptAdmissionKind int

const (
	admissionAdmitted         attemptAdmissionKind = iota // lease armed; attempt observable as active
	admissionBusyAPI                                      // API while active — canonical busy, no queue (req 6.9)
	admissionPendingHUP                                   // first SIGHUP while active sets at most one pending follow-up (req 6.8)
	admissionCoalescedHUP                                 // additional SIGHUP increments coalesced (req 6.8)
	admissionRejectedShutdown                             // shutting down; no lease (req 6.1)
)

type attemptFinishKind int

const (
	finishReleasedIdle     attemptFinishKind = iota // final active lease released; idle waiters wake
	finishFollowUpClaimed                           // claimed one pending HUP; gate stays non-idle
	finishAlreadyCompleted                          // duplicate Complete — panic-free no-op
)

type attemptAdmissionOutcome struct {
	Kind             attemptAdmissionKind
	Lease            *attemptLease // non-nil only for admissionAdmitted
	CoalescedSignals int64
	Category         sdkreload.ResultCategory
	ReasonCategory   string
}

type attemptFinishOutcome struct {
	Kind             attemptFinishKind
	FollowUpLease    *attemptLease
	CoalescedSignals int64
}

type attemptGateSnapshot struct {
	Busy, PendingSignal bool
	CoalescedSignals    int64
}

// attemptGate owns reload busy admission, pending SIGHUP reservation, coalesced
// count, active attempt context/cancellation/completion lease, shutdown rejection,
// and non-polling idle notification (req 6.1, 6.5-6.9).
type attemptGate struct {
	mu                   sync.Mutex
	shutdown, pendingHUP bool
	coalesced            int64
	active               *attemptLease
	nextToken            uint64
	idleNotify           chan struct{} // closed while idle; open while busy
}

type attemptLease struct {
	gate     *attemptGate
	token    uint64
	ctx      context.Context
	cancel   context.CancelFunc
	finished bool // under gate.mu
}

func newAttemptGate() *attemptGate {
	ch := make(chan struct{})
	close(ch)
	return &attemptGate{idleNotify: ch}
}

func shutdownAdmission() attemptAdmissionOutcome {
	return attemptAdmissionOutcome{
		Kind: admissionRejectedShutdown, Category: sdkreload.ResultCanceled,
		ReasonCategory: configreload.StageShutdown,
	}
}

func busyAdmission(kind attemptAdmissionKind, reason string, coal int64) attemptAdmissionOutcome {
	return attemptAdmissionOutcome{
		Kind: kind, CoalescedSignals: coal, Category: sdkreload.ResultBusy,
		ReasonCategory: reason,
	}
}

// TryStart admits one attempt or returns immutable busy/reject outcome (req 6.5, 6.6).
func (g *attemptGate) TryStart(ctx context.Context, trigger sdkreload.Trigger) attemptAdmissionOutcome {
	if g == nil {
		return shutdownAdmission()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.shutdown {
		return shutdownAdmission()
	}
	if g.active != nil {
		if trigger.Kind == sdkreload.TriggerSIGHUP {
			if g.pendingHUP {
				g.coalesced++
				return busyAdmission(admissionCoalescedHUP, configreload.StageCoalesce, g.coalesced)
			}
			g.pendingHUP = true
			return busyAdmission(admissionPendingHUP, configreload.StageCoalesce, 0)
		}
		return busyAdmission(admissionBusyAPI, configreload.StageBusy, 0)
	}
	base := context.WithoutCancel(ctx)
	if base == nil {
		base = context.Background()
	}
	leaseCtx, cancel := context.WithCancel(base)
	g.nextToken++
	lease := &attemptLease{gate: g, token: g.nextToken, ctx: leaseCtx, cancel: cancel}
	g.idleNotify = make(chan struct{})
	g.active = lease
	return attemptAdmissionOutcome{Kind: admissionAdmitted, Lease: lease}
}

// BeginShutdown marks shutdown, clears pending/count, rejects future starts,
// cancels active lease context (req 6.1, 6.7). Repeated/concurrent calls are safe.
func (g *attemptGate) BeginShutdown() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.shutdown, g.pendingHUP, g.coalesced = true, false, 0
	cancel := context.CancelFunc(nil)
	if g.active != nil {
		cancel = g.active.cancel
	}
	g.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// WaitForIdle waits until no attempt is active; must not poll or sleep (req 6.5).
func (g *attemptGate) WaitForIdle(ctx context.Context) error {
	if g == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		g.mu.Lock()
		idle, notify := g.active == nil, g.idleNotify
		g.mu.Unlock()
		if idle {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-notify:
		}
	}
}

func (g *attemptGate) Snapshot() attemptGateSnapshot {
	if g == nil {
		return attemptGateSnapshot{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return attemptGateSnapshot{Busy: g.active != nil, PendingSignal: g.pendingHUP, CoalescedSignals: g.coalesced}
}

func (g *attemptGate) shuttingDown() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.shutdown
}

func (l *attemptLease) Context() context.Context {
	if l == nil || l.ctx == nil {
		return context.Background()
	}
	return l.ctx
}

func (g *attemptGate) releaseActiveIdleLocked(l *attemptLease) context.CancelFunc {
	l.finished = true
	cancel, notify := l.cancel, g.idleNotify
	g.active, g.pendingHUP, g.coalesced = nil, false, 0
	close(notify)
	return cancel
}

func (l *attemptLease) invokeCancel(cancel context.CancelFunc) {
	if cancel != nil {
		cancel()
	}
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
		l.invokeCancel(cancel)
		return attemptFinishOutcome{Kind: finishReleasedIdle}
	}
	l.finished = true
	coal := g.coalesced
	g.pendingHUP, g.coalesced = false, 0
	g.nextToken++
	follow := &attemptLease{gate: g, token: g.nextToken, ctx: l.ctx, cancel: l.cancel}
	g.active = follow
	g.mu.Unlock()
	return attemptFinishOutcome{Kind: finishFollowUpClaimed, FollowUpLease: follow, CoalescedSignals: coal}
}

// Abandon releases this lease without claiming pending follow-up work (req 6.7).
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
	l.invokeCancel(cancel)
}
