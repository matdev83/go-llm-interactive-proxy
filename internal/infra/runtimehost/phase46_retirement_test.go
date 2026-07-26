package runtimehost_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)

// Task 4.6: blocked SSE/stream pin exhausts retained budget → later publish rejects
// without terminating the old stream (req 10.8-10.11).
func TestBlockedStream_RetentionCapRejectsWithoutTerminating(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(1, nil)
	g1 := m.Prepare("stream-gen")
	mustPublish(t, m, g1)

	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	pin, ok := lease.TransferPin(runtimehost.PinSSE)
	if !ok {
		t.Fatal("transfer sse pin")
	}

	mustPublish(t, m, m.Prepare("g2"))
	active := m.Active()
	pressure := m.RetentionPressure()
	if pressure.BlockingCategory != runtimehost.RetentionCategoryBudget {
		t.Fatalf("pressure category=%q want %q", pressure.BlockingCategory, runtimehost.RetentionCategoryBudget)
	}
	if pressure.Retained < 1 || pressure.MaxRetained != 1 {
		t.Fatalf("pressure=%+v", pressure)
	}

	candCloses := atomic.Int32{}
	cand := m.PrepareOwned("g3", &stubOwned{closeFn: func() error {
		candCloses.Add(1)
		return nil
	}})
	err := m.Publish(cand)
	if !errors.Is(err, runtimehost.ErrRetentionBlocked) {
		t.Fatalf("want retention blocked, got %v", err)
	}
	if candCloses.Load() != 1 {
		t.Fatalf("candidate rollback closes=%d want 1", candCloses.Load())
	}
	if m.Active() != active {
		t.Fatal("retention block must not swap active")
	}
	select {
	case <-g1.Drained():
		t.Fatal("blocked stream must not be terminated for retention pressure")
	default:
	}
	if g1.Refs() != 1 {
		t.Fatalf("stream pin refs=%d", g1.Refs())
	}

	pin.Release()
	<-g1.Drained()
	if err := runtimehost.NewLifecycleWorker().Retire(context.Background(), g1, nil); err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatal(err)
	}
	m.SweepClosed()
	mustPublish(t, m, m.Prepare("g3-after-drain"))
}

func TestGeneration_CloseFailure_StaysClosingForRetry(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("close-temp")
	var closes atomic.Int32
	owned := &stubOwned{closeFn: func() error {
		n := closes.Add(1)
		if n == 1 {
			return closeErr
		}
		return nil
	}}
	m := runtimehost.NewManager(2, nil)
	g := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g)
	mustPublish(t, m, m.Prepare("g2"))
	<-g.Drained()
	if err := g.BeginClose(); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("first close: %v", err)
	}
	if g.Lifecycle() != runtimehost.GenClosing {
		t.Fatalf("lifecycle=%v want closing after failed cleanup", g.Lifecycle())
	}
	if err := g.Close(); err != nil {
		t.Fatalf("retry close: %v", err)
	}
	if g.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v want closed", g.Lifecycle())
	}
	if closes.Load() != 2 {
		t.Fatalf("closes=%d want 2 attempts", closes.Load())
	}
	if err := g.Close(); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("post-success close: %v", err)
	}
}

func TestLifecycleWorker_CleanupRetryThenSucceeds(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("cleanup-temp")
	var closes atomic.Int32
	owned := &ledgerOwned{
		closeFn: func() error {
			if closes.Add(1) == 1 {
				return closeErr
			}
			return nil
		},
	}
	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	g2 := m.Prepare("g2")
	mustPublish(t, m, g2)

	worker := runtimehost.NewLifecycleWorkerWithPolicy(runtimehost.CleanupPolicy{MaxAttempts: 3})
	if err := worker.Retire(context.Background(), g1, owned); err != nil {
		t.Fatal(err)
	}
	if owned.closes.Load() != 2 {
		t.Fatalf("closes=%d want 2 (fail then success)", owned.closes.Load())
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v", g1.Lifecycle())
	}
	st := worker.LastStatus()
	if st.Outcome != runtimehost.LifecycleOutcomeOK {
		t.Fatalf("status=%+v", st)
	}
	if m.Active() != g2 || g2.Lifecycle() != runtimehost.GenActive {
		t.Fatal("active must remain healthy")
	}
}

func TestLifecycleWorker_CleanupExhausted_ReportsCleanupFailed(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("cleanup-permanent")
	owned := &ledgerOwned{
		closeFn: func() error { return closeErr },
	}
	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	g2 := m.Prepare("g2")
	mustPublish(t, m, g2)

	worker := runtimehost.NewLifecycleWorkerWithPolicy(runtimehost.CleanupPolicy{MaxAttempts: 2})
	err := worker.Retire(context.Background(), g1, owned)
	if !errors.Is(err, closeErr) {
		t.Fatalf("want cleanup error, got %v", err)
	}
	if g1.Lifecycle() != runtimehost.GenClosing {
		t.Fatalf("lifecycle=%v want closing after exhausted retries", g1.Lifecycle())
	}
	st := worker.LastStatus()
	if st.Outcome != runtimehost.LifecycleOutcomeCleanupFailed {
		t.Fatalf("status outcome=%q want %q", st.Outcome, runtimehost.LifecycleOutcomeCleanupFailed)
	}
	if st.Attempts != 2 {
		t.Fatalf("attempts=%d", st.Attempts)
	}
	if m.Active() != g2 {
		t.Fatal("cleanup failure must not corrupt active")
	}
}

func TestLifecycleWorker_CleanupPanicIsolated(t *testing.T) {
	t.Parallel()
	owned := &ledgerOwned{
		closeFn: func() error { panic("cleanup boom") },
	}
	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	g2 := m.Prepare("g2")
	mustPublish(t, m, g2)

	worker := runtimehost.NewLifecycleWorkerWithPolicy(runtimehost.CleanupPolicy{MaxAttempts: 1})
	err := worker.Retire(context.Background(), g1, owned)
	if err == nil || !strings.Contains(err.Error(), "cleanup boom") {
		t.Fatalf("want isolated panic error, got %v", err)
	}
	if m.Active() != g2 || g2.Lifecycle() != runtimehost.GenActive {
		t.Fatal("cleanup panic must not alter active generation")
	}
	st := worker.LastStatus()
	if st.Outcome != runtimehost.LifecycleOutcomeCleanupFailed {
		t.Fatalf("status=%+v", st)
	}
}

func TestLifecycleWorker_QuiescePanicIsolated_ContinuesDrain(t *testing.T) {
	t.Parallel()
	owned := &ledgerOwned{
		quiesceFn: func(context.Context) error { panic("quiesce boom") },
	}
	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	g2 := m.Prepare("g2")
	mustPublish(t, m, g2)

	worker := runtimehost.NewLifecycleWorker()
	errCh := make(chan error, 1)
	go func() {
		errCh <- worker.Retire(context.Background(), g1, owned)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if owned.quiesces.Load() > 0 || g1.Lifecycle() == runtimehost.GenQuiesced || g1.Lifecycle() == runtimehost.GenDrained {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	lease.Release()

	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "quiesce boom") {
			t.Fatalf("want quiesce panic error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retire timeout")
	}
	if m.Active() != g2 {
		t.Fatal("quiesce panic must leave newer generation active")
	}
	st := worker.LastStatus()
	if st.Outcome != runtimehost.LifecycleOutcomeQuiesceFailed && st.Outcome != runtimehost.LifecycleOutcomeOK {
		// Quiesce failure is recorded; close may still succeed afterward.
		if st.Outcome != runtimehost.LifecycleOutcomeCleanupFailed {
			t.Fatalf("unexpected status=%+v", st)
		}
	}
	if owned.closes.Load() != 1 {
		t.Fatalf("close should still run after quiesce panic isolation, closes=%d", owned.closes.Load())
	}
}

func TestLifecycleWorker_QuiesceError_StatusReporting(t *testing.T) {
	t.Parallel()
	quiesceErr := errors.New("quiesce-failed")
	owned := &ledgerOwned{
		quiesceFn: func(context.Context) error { return quiesceErr },
	}
	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	mustPublish(t, m, m.Prepare("g2"))

	worker := runtimehost.NewLifecycleWorker()
	err := worker.Retire(context.Background(), g1, owned)
	if !errors.Is(err, quiesceErr) {
		t.Fatalf("want quiesce error, got %v", err)
	}
	st := worker.LastStatus()
	if st.Outcome != runtimehost.LifecycleOutcomeQuiesceFailed {
		t.Fatalf("status=%+v", st)
	}
	if st.GenerationID != g1.ID() {
		t.Fatalf("status gen=%d want %d", st.GenerationID, g1.ID())
	}
}
