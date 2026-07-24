package runtimehost

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/configreload"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

// Task 6.1 deterministic concurrency characterization against current Coordinator
// gate primitives. Barriers and channels only — no wall-clock synchronization.
// Context timeouts appear only as post-barrier deadlock guards.
// A past time.Time used solely to build an already-expired deadline is allowed.

func TestAttemptGate_BusyAPIConflictNoPending(t *testing.T) {
	t.Parallel()
	c := &Coordinator{}
	c.mu.Lock()
	c.busy = true
	c.mu.Unlock()

	res := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if res.Category != sdkreload.ResultBusy {
		t.Fatalf("API while busy category=%q want busy", res.Category)
	}
	if res.ReasonCategory != configreload.StageBusy {
		t.Fatalf("API while busy reason=%q want %q", res.ReasonCategory, configreload.StageBusy)
	}
	if res.CoalescedSignals != 0 {
		t.Fatalf("API busy coalesced on result=%d want 0", res.CoalescedSignals)
	}
	st := c.Status()
	if !st.Busy {
		t.Fatal("must remain busy")
	}
	if st.PendingSignal {
		t.Fatal("API busy must not queue a pending follow-up (req 6.9)")
	}
	if st.CoalescedSignals != 0 {
		t.Fatalf("API busy must not coalesce; got %d", st.CoalescedSignals)
	}
}

func TestAttemptGate_CoalesceBoundedPending(t *testing.T) {
	t.Parallel()
	c := &Coordinator{}
	c.mu.Lock()
	c.busy = true
	c.mu.Unlock()

	first := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if first.Category != sdkreload.ResultBusy {
		t.Fatalf("first HUP category=%q", first.Category)
	}
	if first.ReasonCategory != configreload.StageCoalesce {
		t.Fatalf("first HUP reason=%q want coalesce", first.ReasonCategory)
	}
	if first.CoalescedSignals != 0 {
		t.Fatalf("first HUP result coalesced=%d want 0", first.CoalescedSignals)
	}
	st := c.Status()
	if !st.PendingSignal {
		t.Fatal("first HUP must create at most one pending follow-up (req 6.8)")
	}
	if st.CoalescedSignals != 0 {
		t.Fatalf("first HUP coalesced=%d want 0", st.CoalescedSignals)
	}

	second := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if second.Category != sdkreload.ResultBusy || second.ReasonCategory != configreload.StageCoalesce {
		t.Fatalf("second HUP=%+v", second)
	}
	if second.CoalescedSignals != 1 {
		t.Fatalf("second HUP coalesced signals=%d want 1", second.CoalescedSignals)
	}
	st = c.Status()
	if !st.PendingSignal {
		t.Fatal("pending must remain singular")
	}
	if st.CoalescedSignals != 1 {
		t.Fatalf("coalesced count=%d want 1", st.CoalescedSignals)
	}

	third := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if third.CoalescedSignals != 2 {
		t.Fatalf("third HUP coalesced=%d want 2", third.CoalescedSignals)
	}
	st = c.Status()
	if !st.PendingSignal || st.CoalescedSignals != 2 {
		t.Fatalf("bounded coalesce status=%+v", st)
	}
}

func TestAttemptGate_ShutdownRejectsAndCancels(t *testing.T) {
	t.Parallel()
	c := &Coordinator{}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, shut := c.armAttempt(cancel)
	if shut {
		t.Fatal("unexpected pre-shutdown arm")
	}
	c.mu.Lock()
	c.busy = true
	c.pendingSignal = true
	c.coalesced = 3
	c.mu.Unlock()

	c.BeginShutdown()

	select {
	case <-parent.Done():
	default:
		t.Fatal("BeginShutdown must cancel active attempt lease")
	}
	st := c.Status()
	if st.PendingSignal {
		t.Fatal("BeginShutdown must clear pending follow-up")
	}

	late := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if late.Category != sdkreload.ResultCanceled {
		t.Fatalf("post-shutdown API=%q want canceled", late.Category)
	}
	if late.ReasonCategory != configreload.StageShutdown {
		t.Fatalf("post-shutdown reason=%q", late.ReasonCategory)
	}
	lateHUP := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if lateHUP.Category != sdkreload.ResultCanceled {
		t.Fatalf("post-shutdown HUP=%q want canceled", lateHUP.Category)
	}

	// Clear busy before release so WaitForIdle observers never enter the
	// production busy-before-armed polling window.
	finishAttempt(c, done, cancel)
}

func TestAttemptGate_ExactFinishSequentialDuplicate(t *testing.T) {
	t.Parallel()
	// Sequential duplicate release is safe on the sync.Once path once done is
	// closed. Concurrent duplicate Complete that races into the else-branch
	// close is source-level AST RED only (see ArchitectureREDInventory) — this
	// test deliberately does not execute a flaky panic race against current code.
	c := &Coordinator{}
	_, cancel := context.WithCancel(context.Background())
	done, _ := c.armAttempt(cancel)

	c.releaseAttempt(done, cancel)
	c.releaseAttempt(done, cancel) // sequential duplicate
	c.releaseAttempt(done, cancel)

	select {
	case <-done:
	default:
		t.Fatal("completion channel must close after Finish/release")
	}
	select {
	case <-done:
	default:
		t.Fatal("done must remain closed")
	}

	// Concurrent waiters observing the already-closed completion must not panic.
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-done
		}()
	}
	wg.Wait()
}

func TestAttemptGate_IdleWaitersWake(t *testing.T) {
	t.Parallel()
	c := &Coordinator{}
	_, cancel := context.WithCancel(context.Background())
	done, _ := c.armAttempt(cancel)
	c.mu.Lock()
	c.busy = true
	c.mu.Unlock()

	const waiters = 8
	reached := make([]<-chan struct{}, waiters)
	errs := make(chan error, waiters)
	var wg sync.WaitGroup
	wg.Add(waiters)
	for i := range waiters {
		bctx, ch := newWaitIdleSelectBarrierCtx(context.Background())
		reached[i] = ch
		go func(ctx context.Context) {
			defer wg.Done()
			errs <- c.WaitForIdle(ctx)
		}(bctx)
	}
	awaitWaitIdleBarriers(t, reached...)

	finishAttempt(c, done, cancel)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("waiter err=%v", err)
		}
	}
}

func TestAttemptGate_IdleAlreadyIdle(t *testing.T) {
	t.Parallel()
	c := &Coordinator{}
	if err := c.WaitForIdle(context.Background()); err != nil {
		t.Fatalf("already idle: %v", err)
	}
}

func TestAttemptGate_IdleCanceledContext(t *testing.T) {
	t.Parallel()
	c := &Coordinator{}
	_, cancel := context.WithCancel(context.Background())
	done, _ := c.armAttempt(cancel)
	c.mu.Lock()
	c.busy = true
	c.mu.Unlock()

	ctx, ctxCancel := context.WithCancel(context.Background())
	ctxCancel() // cancel before wait — deterministic, no scheduling race
	err := c.WaitForIdle(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled WaitForIdle err=%v want context.Canceled", err)
	}
	finishAttempt(c, done, cancel)
}

func TestAttemptGate_IdleDeadlineExceeded(t *testing.T) {
	t.Parallel()
	c := &Coordinator{}
	_, cancel := context.WithCancel(context.Background())
	done, _ := c.armAttempt(cancel)
	c.mu.Lock()
	c.busy = true
	c.mu.Unlock()

	// Past deadline only — not synchronization. time.Time{} is an already-expired deadline.
	ctx, ctxCancel := context.WithDeadline(context.Background(), time.Time{})
	defer ctxCancel()
	err := c.WaitForIdle(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired-deadline WaitForIdle err=%v want context.DeadlineExceeded", err)
	}
	finishAttempt(c, done, cancel)
}

func TestAttemptGate_BusyBeforeArmedREDWindow(t *testing.T) {
	t.Parallel()
	// Deterministic split without scheduler timing: current Coordinator can make
	// busy visible while attemptDone is still nil (req 6.5/6.6 violation).
	c := &Coordinator{}
	c.mu.Lock()
	c.busy = true
	busy := c.busy
	armed := c.attemptDone
	c.mu.Unlock()

	if !busy {
		t.Fatal("expected busy")
	}
	if armed != nil {
		t.Fatal("RED window requires attemptDone nil while busy")
	}
	st := c.Status()
	if !st.Busy {
		t.Fatal("Status must expose busy before completion is armed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.WaitForIdle(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("unarmed busy WaitForIdle err=%v want canceled", err)
	}
	t.Log("RED: current split busy/armAttempt flow allows busy-before-armed observation")
}

func TestAttemptGate_IdleAfterActiveFinish(t *testing.T) {
	t.Parallel()
	c := &Coordinator{}
	_, cancel := context.WithCancel(context.Background())
	done, _ := c.armAttempt(cancel)
	c.mu.Lock()
	c.busy = true
	c.mu.Unlock()

	bctx, reached := newWaitIdleSelectBarrierCtx(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.WaitForIdle(bctx)
	}()
	awaitWaitIdleBarriers(t, reached)

	finishAttempt(c, done, cancel)
	if err := <-errCh; err != nil {
		t.Fatalf("active→finish WaitForIdle: %v", err)
	}
}

func TestAttemptGate_ShutdownCancelsIdleWaiters(t *testing.T) {
	t.Parallel()
	c := &Coordinator{}
	parent, cancel := context.WithCancel(context.Background())
	done, _ := c.armAttempt(cancel)
	c.mu.Lock()
	c.busy = true
	c.mu.Unlock()

	// Idle waiter uses a distinct context; BeginShutdown cancels the lease, and
	// Finish/release wakes waiters (current Coordinator does not close done on
	// shutdown alone — Task 6.2 gate must keep wake-on-finish semantics).
	bctx, reached := newWaitIdleSelectBarrierCtx(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.WaitForIdle(bctx)
	}()
	awaitWaitIdleBarriers(t, reached)

	c.BeginShutdown()
	select {
	case <-parent.Done():
	default:
		t.Fatal("shutdown must cancel lease context")
	}
	finishAttempt(c, done, cancel)
	if err := <-errCh; err != nil {
		t.Fatalf("idle after shutdown+finish: %v", err)
	}
}

func TestAttemptGate_InterleavingsOneCompleteVsWaitShutdown(t *testing.T) {
	t.Parallel()
	// One completion racing with WaitForIdle + BeginShutdown (and a busy-path
	// hammer while still busy). Concurrent duplicate Complete is NOT executed
	// here — that remains source-level AST RED until Task 6.2.
	const iters = 100
	for i := range iters {
		c := &Coordinator{}
		_, cancel := context.WithCancel(context.Background())
		done, _ := c.armAttempt(cancel)
		c.mu.Lock()
		c.busy = true
		c.mu.Unlock()

		var wg sync.WaitGroup
		var waitErrs atomic.Int64

		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := c.WaitForIdle(context.Background()); err != nil {
					waitErrs.Add(1)
				}
			}()
		}

		// Busy-path hammer must finish before busy is cleared. A Reload that
		// observes !busy can arm a new attemptDone and strand WaitForIdle
		// waiters on the current Coordinator (explicit Task 6.2 atomic-arm gap).
		var hammer sync.WaitGroup
		hammer.Add(1)
		go func() {
			defer hammer.Done()
			_ = c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
			_ = c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
			_ = c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
		}()
		hammer.Wait()

		wg.Add(1)
		go func() {
			defer wg.Done()
			c.BeginShutdown()
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			finishAttempt(c, done, cancel)
		}()

		wg.Wait()
		if waitErrs.Load() != 0 {
			t.Fatalf("iter %d: WaitForIdle errors=%d", i, waitErrs.Load())
		}
		select {
		case <-done:
		default:
			t.Fatalf("iter %d: done not closed", i)
		}
		late := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
		if late.Category != sdkreload.ResultCanceled {
			t.Fatalf("iter %d late start=%q", i, late.Category)
		}
	}
}

// finishAttempt clears busy before closing the completion channel so idle
// waiters observe a single causal transition without entering WaitForIdle's
// production busy-before-armed polling branch.
func finishAttempt(c *Coordinator, done chan struct{}, cancel context.CancelFunc) {
	c.mu.Lock()
	c.busy = false
	c.pendingSignal = false
	c.mu.Unlock()
	c.releaseAttempt(done, cancel)
}

// waitIdleSelectBarrierCtx is a deterministic observation barrier for WaitForIdle.
// Done() signals once when WaitForIdle has entered the armed select (after reading
// the active completion channel) and returns a never-closed channel so only the
// completion signal (or a wrapping parent cancel, unused here) can unblock.
type waitIdleSelectBarrierCtx struct {
	parent  context.Context
	reached chan struct{}
	once    sync.Once
	never   chan struct{}
}

func newWaitIdleSelectBarrierCtx(parent context.Context) (*waitIdleSelectBarrierCtx, <-chan struct{}) {
	if parent == nil {
		parent = context.Background()
	}
	reached := make(chan struct{})
	return &waitIdleSelectBarrierCtx{
		parent:  parent,
		reached: reached,
		never:   make(chan struct{}),
	}, reached
}

func (c *waitIdleSelectBarrierCtx) Deadline() (time.Time, bool) { return c.parent.Deadline() }

func (c *waitIdleSelectBarrierCtx) Done() <-chan struct{} {
	c.once.Do(func() { close(c.reached) })
	return c.never
}

func (c *waitIdleSelectBarrierCtx) Err() error { return nil }

func (c *waitIdleSelectBarrierCtx) Value(key any) any { return c.parent.Value(key) }

// awaitWaitIdleBarriers waits until every waiter has reached WaitForIdle's armed
// select. Context timeout is only a post-setup deadlock guard — not synchronization.
func awaitWaitIdleBarriers(t *testing.T, reached ...<-chan struct{}) {
	t.Helper()
	guard, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i, ch := range reached {
		select {
		case <-ch:
		case <-guard.Done():
			t.Fatalf("deadlock: waiter %d did not reach WaitForIdle armed select barrier", i)
		}
	}
}
