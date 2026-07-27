package acp

import (
	"sync"
	"testing"
	"time"
)

// waitFor polls cond every pollInterval until it returns true or deadline
// elapses. Reports via t.Fatalf with msg if the deadline elapses first.
// Skill golang-testing: tests MUST NOT depend on wall-time sleeps, so the
// pool's stale-kill / idle-reap timing is observed by polling the
// observable outcome instead of fixed-duration sleeps.
func waitFor(t *testing.T, deadline, pollInterval time.Duration, msg string, cond func() bool) {
	t.Helper()
	if deadline <= 0 {
		deadline = 2 * time.Second
	}
	if pollInterval <= 0 {
		pollInterval = 5 * time.Millisecond
	}
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if cond() {
			return
		}
		select {
		case <-timer.C:
			t.Fatalf("waitFor: %s (deadline %s)", msg, deadline)
		case <-ticker.C:
		}
	}
}

func TestRuntimePool_AcquireCreatesRuntime(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	rt, err := pool.Acquire(key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if rt == nil {
		t.Fatal("expected non-nil runtime")
	}
	if rt.HasProcess() {
		t.Fatal("new runtime should not have a process")
	}
	if rt.IsInitialized() {
		t.Fatal("new runtime should not be initialized")
	}
}

func TestRuntimePool_AcquireReturnsSameRuntime(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	rt1, err := pool.Acquire(key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	rt2, err := pool.Acquire(key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if rt1 != rt2 {
		t.Fatal("expected same runtime for same key")
	}
}

func TestRuntimePool_AcquireDifferentKeysDifferentRuntimes(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key1 := RuntimeKey{Workspace: "/tmp/a", Model: "agent", ClientSession: "s1"}
	key2 := RuntimeKey{Workspace: "/tmp/b", Model: "agent", ClientSession: "s1"}
	rt1, err := pool.Acquire(key1)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	rt2, err := pool.Acquire(key2)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if rt1 == rt2 {
		t.Fatal("expected different runtimes for different keys")
	}
}

func TestRuntimePool_SetProcessAndSessionID(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)

	proc := newFakeProcess(t)
	pool.SetProcess(key, proc, nil, "session-123", "", "cursor-agent")

	rt := pool.Get(key)
	if rt == nil {
		t.Fatal("expected non-nil runtime after SetProcess")
	}
	if !rt.HasProcess() {
		t.Fatal("expected runtime to have a process")
	}
	if !rt.IsInitialized() {
		t.Fatal("expected runtime to be initialized")
	}
	if rt.SessionID() != "session-123" {
		t.Fatalf("SessionID = %q, want %q", rt.SessionID(), "session-123")
	}
}

func TestRuntimePool_IdleReaping(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{
		IdleTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	rt, err := pool.Acquire(key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	proc := newFakeProcess(t)
	pool.SetProcess(key, proc, nil, "session-1", "", "cursor-agent")
	pool.Release(key)

	// Wait until the pool reaps the idle runtime and replaces it with a
	// fresh slot. The cond is a pure predicate; assertions live after
	// waitFor so a "not yet" tick does not call t.Fatal in a loop. The
	// last Acquire error is captured so `go test -v` shows the real
	// failure mode if the deadline elapses inside the polling loop; the
	// nil-clear on the no-error branch resets stale state from any prior
	// tick that errored (so a recovered Acquire does not leave a ghost
	// error behind when cond finally succeeds).
	var rt2 *SubprocessRuntime
	var lastAcquireErr error
	waitFor(t, 0, 0, "Acquire returns fresh runtime after idle reaping", func() bool {
		got, err := pool.Acquire(key)
		if err != nil {
			lastAcquireErr = err
			return false
		}
		lastAcquireErr = nil
		if got == rt {
			return false // still the original runtime; hasn't been reaped yet
		}
		rt2 = got
		return true
	})
	if lastAcquireErr != nil {
		t.Fatalf("Acquire errored during idle reaping wait: %v", lastAcquireErr)
	}
	if rt2.HasProcess() {
		t.Fatal("fresh runtime should not have a process")
	}
}

func TestRuntimePool_NoReapingWhileInUse(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{
		IdleTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	rt, err := pool.Acquire(key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	proc := newFakeProcess(t)
	pool.SetProcess(key, proc, nil, "session-1", "", "cursor-agent")
	pool.MarkInUse(key)

	// While marked in-use, Acquire must return the same runtime — no
	// reaping, no replacement. The existing logic is synchronous under
	// the pool lock, so a single Acquire call suffices.
	rt2, _ := pool.Acquire(key)
	if rt2 != rt {
		t.Fatal("expected same runtime while in use")
	}
}

func TestRuntimePool_KillRuntime(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)
	proc := newFakeProcess(t)
	pool.SetProcess(key, proc, nil, "session-1", "", "cursor-agent")

	if err := pool.KillRuntime(key); err != nil {
		t.Fatalf("KillRuntime: %v", err)
	}

	rt := pool.Get(key)
	if rt == nil {
		t.Fatal("runtime should still exist after kill")
	}
	if rt.HasProcess() {
		t.Fatal("runtime should not have a process after kill")
	}
	if rt.IsInitialized() {
		t.Fatal("runtime should not be initialized after kill")
	}
}

func TestRuntimePool_StaleKillAfterRelease(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{
		StaleKillDelay: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)
	proc := newFakeProcess(t)
	pool.SetProcess(key, proc, nil, "session-1", "", "cursor-agent")
	pool.Release(key)

	// Poll for both observable outcomes: the runtime slot no longer holds
	// a process AND the underlying fake reported itself killed.
	waitFor(t, 0, 0, "stale kill timer fires and clears fake process", func() bool {
		rt := pool.Get(key)
		if rt == nil || rt.HasProcess() {
			return false
		}
		proc.mu.Lock()
		defer proc.mu.Unlock()
		return proc.killed
	})

	if rt := pool.Get(key); rt == nil {
		t.Fatal("runtime should still exist after stale kill")
	}
}

func TestRuntimePool_StaleKillCancelledByAcquire(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{
		StaleKillDelay: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)
	proc := newFakeProcess(t)
	pool.SetProcess(key, proc, nil, "session-1", "", "cursor-agent")
	pool.Release(key)

	// Mark the runtime in-use synchronously so the freshly armed stale-kill
	// timer (50ms) is cancelled before it can fire. Poll that the process
	// survives well past the would-be kill moment — cancellation must work.
	markAt := time.Now()
	pool.MarkInUse(key)
	waitFor(t, 0, 0, "process still alive past StaleKillDelay after MarkInUse cancellation", func() bool {
		if time.Since(markAt) < 100*time.Millisecond {
			return false
		}
		rt := pool.Get(key)
		if rt == nil || !rt.HasProcess() {
			return false
		}
		proc.mu.Lock()
		defer proc.mu.Unlock()
		return !proc.killed
	})
}

func TestRuntimePool_ConcurrentAcquire(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			rt, err := pool.Acquire(key)
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			if rt == nil {
				t.Error("expected non-nil runtime")
			}
		})
	}
	wg.Wait()
}

func TestRuntimePool_CloseKillsAll(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})

	key1 := RuntimeKey{Workspace: "/tmp/a", Model: "agent", ClientSession: "s1"}
	key2 := RuntimeKey{Workspace: "/tmp/b", Model: "agent", ClientSession: "s2"}
	_, _ = pool.Acquire(key1)
	_, _ = pool.Acquire(key2)
	pool.SetProcess(key1, newFakeProcess(t), nil, "s1", "", "agent1")
	pool.SetProcess(key2, newFakeProcess(t), nil, "s2", "", "agent2")

	if err := pool.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Pool should be cleared.
	if rt := pool.Get(key1); rt != nil {
		t.Fatal("expected nil runtime after Close")
	}
}

func TestRuntimePool_HistoryState(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	rt, _ := pool.Acquire(key)

	initial := rt.HistoryState()
	if initial.messageCount != 0 || initial.prefixHash != "" {
		t.Fatalf("initial history state = %+v, want zero", initial)
	}

	rt.SetHistoryState(historyState{messageCount: 5, prefixHash: "abc123"})
	got := rt.HistoryState()
	if got.messageCount != 5 || got.prefixHash != "abc123" {
		t.Fatalf("history state = %+v, want {5, abc123}", got)
	}
}

func TestRuntimeKey_String(t *testing.T) {
	t.Parallel()
	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	s := key.String()
	if s == "" {
		t.Fatal("expected non-empty string representation")
	}
}

func TestRuntimePool_StaleKillPIDReuseHardening(t *testing.T) {
	t.Parallel()
	pool := NewRuntimePool(RuntimePoolConfig{
		StaleKillDelay: 30 * time.Millisecond,
	})
	t.Cleanup(func() { _ = pool.Close() })

	key := RuntimeKey{Workspace: "/tmp/proj", Model: "agent", ClientSession: "s1"}
	_, _ = pool.Acquire(key)
	proc := newFakeProcess(t)
	pool.SetProcess(key, proc, nil, "session-1", "", "cursor-agent")

	// Manually corrupt the identity to simulate PID reuse (different PID).
	rt := pool.Get(key)
	rt.mu.Lock()
	rt.identity = ProcessIdentity{PID: proc.PID() + 99999}
	rt.mu.Unlock()

	pool.Release(key)

	// Poll for the observable outcome: KillRuntime resets state but skips
	// the kill because identity mismatches. The fake process must remain
	// alive (proc.killed == false) once the timer fires.
	waitFor(t, 0, 0, "KillRuntime clears state without killing the reused-PID process", func() bool {
		rt := pool.Get(key)
		if rt == nil || rt.HasProcess() {
			return false
		}
		proc.mu.Lock()
		defer proc.mu.Unlock()
		// Timer has run and reset state; identity check kept kill off.
		return !proc.killed
	})
}
