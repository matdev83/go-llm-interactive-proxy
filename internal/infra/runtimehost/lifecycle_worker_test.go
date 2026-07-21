package runtimehost_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)

type ledgerOwned struct {
	quiesceFn func(context.Context) error
	closeFn   func() error

	quiesces atomic.Int32
	closes   atomic.Int32
}

func (l *ledgerOwned) Quiesce(ctx context.Context) error {
	l.quiesces.Add(1)
	if l.quiesceFn != nil {
		return l.quiesceFn(ctx)
	}
	return nil
}

func (l *ledgerOwned) Close() error {
	l.closes.Add(1)
	if l.closeFn != nil {
		return l.closeFn()
	}
	return nil
}

func TestLifecycleWorker_QuiesceDrainCloseOnceOutsidePublish(t *testing.T) {
	t.Parallel()
	owned := &ledgerOwned{}
	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)

	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}

	publishDone := make(chan struct{})
	go func() {
		mustPublish(t, m, m.Prepare("g2"))
		close(publishDone)
	}()

	select {
	case <-publishDone:
	case <-time.After(2 * time.Second):
		t.Fatal("publication must not wait for quiesce/drain")
	}
	if g1.Lifecycle() != runtimehost.GenRetiring {
		t.Fatalf("lifecycle=%v", g1.Lifecycle())
	}
	if owned.quiesces.Load() != 0 || owned.closes.Load() != 0 {
		t.Fatal("publish must not quiesce or close")
	}

	worker := runtimehost.NewLifecycleWorker()
	errCh := make(chan error, 1)
	go func() {
		errCh <- worker.Retire(context.Background(), g1, owned)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := g1.Lifecycle()
		if st == runtimehost.GenQuiescing || st == runtimehost.GenQuiesced {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	st := g1.Lifecycle()
	if st != runtimehost.GenQuiescing && st != runtimehost.GenQuiesced {
		t.Fatalf("want quiescing/quiesced while lease held, got %v", st)
	}
	lease.Release()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retire timeout")
	}
	if owned.quiesces.Load() != 1 || owned.closes.Load() != 1 {
		t.Fatalf("quiesces=%d closes=%d", owned.quiesces.Load(), owned.closes.Load())
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v", g1.Lifecycle())
	}
	if err := worker.Retire(context.Background(), g1, owned); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("second retire: %v", err)
	}
	if owned.quiesces.Load() != 1 || owned.closes.Load() != 1 {
		t.Fatalf("idempotent quiesces=%d closes=%d", owned.quiesces.Load(), owned.closes.Load())
	}
}

func TestLifecycleWorker_BlockedQuiesceInterleaving_NoEarlyClose(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	quiesceEntered := make(chan struct{})
	releaseQuiesce := make(chan struct{})
	owned := &ledgerOwned{
		quiesceFn: func(context.Context) error {
			close(quiesceEntered)
			<-releaseQuiesce
			return nil
		},
	}
	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	mustPublish(t, m, m.Prepare("g2"))

	worker := runtimehost.NewLifecycleWorker()
	errCh := make(chan error, 1)
	go func() {
		errCh <- worker.Retire(context.Background(), g1, owned)
	}()
	<-quiesceEntered

	// Final ref release during quiesce must not drain/close early (task 3.1).
	lease.Release()
	if g1.Lifecycle() != runtimehost.GenQuiescing {
		t.Fatalf("lifecycle=%v want still quiescing", g1.Lifecycle())
	}
	select {
	case <-g1.Drained():
		t.Fatal("must not drain until MarkQuiesced")
	default:
	}
	mu.Lock()
	if owned.closes.Load() != 0 {
		mu.Unlock()
		t.Fatal("close before quiesce completes")
	}
	mu.Unlock()

	close(releaseQuiesce)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retire timeout")
	}
	if owned.quiesces.Load() != 1 || owned.closes.Load() != 1 {
		t.Fatalf("quiesces=%d closes=%d", owned.quiesces.Load(), owned.closes.Load())
	}
}

func TestLifecycleWorker_IndependentGenerationRetirementConcurrency(t *testing.T) {
	t.Parallel()
	owned1 := &ledgerOwned{}
	owned2 := &ledgerOwned{}
	m := runtimehost.NewManager(3, nil)
	g1 := m.PrepareOwned("g1", owned1)
	mustPublish(t, m, g1)

	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire g1 lease")
	}

	g2 := m.PrepareOwned("g2", owned2)
	mustPublish(t, m, g2)
	g3 := m.Prepare("g3")
	mustPublish(t, m, g3)

	if g1.Lifecycle() != runtimehost.GenRetiring {
		t.Fatalf("g1 lifecycle=%v want retiring (lease held)", g1.Lifecycle())
	}
	// Zero-ref retirement may already be GenDrained before the worker runs.
	switch g2.Lifecycle() {
	case runtimehost.GenRetiring, runtimehost.GenDrained:
	default:
		t.Fatalf("g2 lifecycle=%v want retiring or drained", g2.Lifecycle())
	}

	worker := runtimehost.NewLifecycleWorker()
	g1Done := make(chan error, 1)
	g1Quiesced := make(chan struct{})
	go func() {
		g1Done <- worker.Retire(context.Background(), g1, owned1)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := g1.Lifecycle()
		if st == runtimehost.GenQuiescing || st == runtimehost.GenQuiesced {
			close(g1Quiesced)
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-g1Quiesced:
	default:
		t.Fatalf("g1 must enter quiesce while lease held, got %v", g1.Lifecycle())
	}

	// g2 has zero refs: must fully quiesce/close while g1 still waits on drain.
	g2Done := make(chan error, 1)
	go func() {
		g2Done <- worker.Retire(context.Background(), g2, owned2)
	}()
	select {
	case err := <-g2Done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("g2 retirement must not block behind g1 drain wait")
	}
	if owned2.quiesces.Load() != 1 || owned2.closes.Load() != 1 {
		t.Fatalf("g2 quiesces=%d closes=%d", owned2.quiesces.Load(), owned2.closes.Load())
	}
	if g2.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("g2 lifecycle=%v", g2.Lifecycle())
	}
	if owned1.closes.Load() != 0 {
		t.Fatal("g1 must still be waiting on held lease before release")
	}
	select {
	case <-g1Done:
		t.Fatal("g1 must not finish retire before lease release")
	default:
	}

	lease.Release()
	select {
	case err := <-g1Done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("g1 retire timeout after release")
	}
	if owned1.quiesces.Load() != 1 || owned1.closes.Load() != 1 {
		t.Fatalf("g1 quiesces=%d closes=%d", owned1.quiesces.Load(), owned1.closes.Load())
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("g1 lifecycle=%v", g1.Lifecycle())
	}

	// Same-generation retirement stays serialized/idempotent and closes once.
	if err := worker.Retire(context.Background(), g2, owned2); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("second g2 retire: %v", err)
	}
	if owned2.quiesces.Load() != 1 || owned2.closes.Load() != 1 {
		t.Fatalf("idempotent g2 quiesces=%d closes=%d", owned2.quiesces.Load(), owned2.closes.Load())
	}
}

func TestLifecycleWorker_QuiesceErrorDoesNotCorruptActive(t *testing.T) {
	t.Parallel()
	quiesceErr := errors.New("quiesce-failed")
	owned := &ledgerOwned{
		quiesceFn: func(context.Context) error { return quiesceErr },
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
	// Allow quiesce to run while lease held, then release for drain/close.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if owned.quiesces.Load() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	lease.Release()

	select {
	case err := <-errCh:
		if !errors.Is(err, quiesceErr) {
			t.Fatalf("want quiesce error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retire timeout")
	}
	if m.Active() != g2 || g2.Lifecycle() != runtimehost.GenActive {
		t.Fatalf("active must remain g2: active=%v life=%v", m.Active(), g2.Lifecycle())
	}
	if owned.quiesces.Load() != 1 {
		t.Fatalf("quiesces=%d", owned.quiesces.Load())
	}
}
