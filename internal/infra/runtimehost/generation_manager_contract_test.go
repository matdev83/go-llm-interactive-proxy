package runtimehost_test

import (
	"errors"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Task 1.4: deterministic generation-manager linearizability/retention contracts.
// Barriers/channels/fake clocks only — no timing sleeps.
func TestPublish_Acquire_LinearizablePointerRecheck(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewRefGenerationManager(4, runtimehost.NewManualClock(time.Unix(1_700_000_000, 0)))
	g1 := m.PrepareCandidate("g1")
	mustPublish(t, m, g1)
	retained := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	m.SetAfterRetainHook(func(g *runtimehost.RefGeneration) {
		if g.ID != 1 {
			return
		}
		once.Do(func() { close(retained); <-resume })
	})
	result := make(chan int64, 1)
	go func() {
		lease, ok := m.Acquire()
		if !ok {
			result <- -1
			return
		}
		id := lease.Generation().ID
		lease.Release()
		result <- id
	}()
	<-retained
	mustPublish(t, m, m.PrepareCandidate("g2"))
	if g1.Lifecycle() != runtimehost.GenRetiring {
		t.Fatalf("g1 lifecycle=%v want retiring", g1.Lifecycle())
	}
	close(resume)
	if id := <-result; id != 2 {
		t.Fatalf("pointer recheck must bind g2, got %d", id)
	}
	m.SetAfterRetainHook(nil)
	_ = m.ClockNow()
	// Barrier-synchronized acquire/publish noise.
	const workers = 32
	start := make(chan struct{})
	var sawOld, sawNew, failed atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-start
			lease, ok := m.Acquire()
			if !ok {
				failed.Add(1)
				return
			}
			id := lease.Generation().ID
			lease.Release()
			switch id {
			case 2:
				sawOld.Add(1)
			case 3:
				sawNew.Add(1)
			default:
				failed.Add(1)
			}
		}()
	}
	pubErr := make(chan error, 1)
	go func() { <-start; pubErr <- m.Publish(m.PrepareCandidate("g3")) }()
	close(start)
	wg.Wait()
	if err := <-pubErr; err != nil {
		t.Fatal(err)
	}
	if failed.Load() != 0 || sawOld.Load()+sawNew.Load() != workers {
		t.Fatalf("race failed=%d old=%d new=%d", failed.Load(), sawOld.Load(), sawNew.Load())
	}
}
func TestAcquire_RetiringBitRefcountAndNoNewRetain(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewRefGenerationManager(4, nil)
	g1 := m.PrepareCandidate("g1")
	mustPublish(t, m, g1)
	hold := make(chan struct{})
	started := make(chan struct{})
	var held atomic.Pointer[runtimehost.RefLease]
	go func() {
		lease, ok := m.Acquire()
		if ok {
			held.Store(lease)
		}
		close(started)
		<-hold
		if lease != nil {
			lease.Release()
		}
	}()
	<-started
	if held.Load() == nil {
		t.Fatal("hold acquire failed")
	}
	l2, ok := m.Acquire()
	if !ok {
		t.Fatal("second acquire")
	}
	if g1.Refs() != 2 {
		t.Fatalf("refs=%d want 2", g1.Refs())
	}
	mustPublish(t, m, m.PrepareCandidate("g2"))
	if g1.Lifecycle() != runtimehost.GenRetiring {
		t.Fatalf("lifecycle=%v", g1.Lifecycle())
	}
	if held.Load().Generation().ID != 1 {
		t.Fatal("pre-publish lease must stay on g1")
	}
	late := make(chan struct{})
	var lateOnG1 atomic.Int64
	var wg sync.WaitGroup
	wg.Add(16)
	for i := 0; i < 16; i++ {
		go func() {
			defer wg.Done()
			<-late
			lease, ok := m.Acquire()
			if !ok {
				return
			}
			if lease.Generation().ID == 1 {
				lateOnG1.Add(1)
			}
			lease.Release()
		}()
	}
	close(late)
	wg.Wait()
	if lateOnG1.Load() != 0 {
		t.Fatalf("new acquires entered retiring g1: %d", lateOnG1.Load())
	}
	l2.Release()
	select {
	case <-g1.Drained():
		t.Fatal("must not drain while hold remains")
	default:
	}
	close(hold)
	<-g1.Drained()
	if g1.Lifecycle() != runtimehost.GenDrained || g1.Refs() != 0 {
		t.Fatalf("drain state=%v refs=%d", g1.Lifecycle(), g1.Refs())
	}
}
func TestRelease_ExactlyOnceDoubleCloseAndAsyncPinTransfer(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewRefGenerationManager(4, nil)
	g1 := m.PrepareCandidate("g1")
	mustPublish(t, m, g1)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	lease.Release()
	lease.Release()
	if g1.Refs() != 0 {
		t.Fatalf("double-close underflow refs=%d", g1.Refs())
	}
	lease, ok = m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	pin, ok := lease.TransferPin(runtimehost.PinAsync)
	if !ok {
		t.Fatal("transfer")
	}
	lease.Release() // transferred; must not drop
	if g1.Refs() != 1 || pin.Kind() != runtimehost.PinAsync {
		t.Fatalf("pin refs=%d kind=%v", g1.Refs(), pin.Kind())
	}
	mustPublish(t, m, m.PrepareCandidate("g2"))
	select {
	case <-g1.Drained():
		t.Fatal("async pin must block drain")
	default:
	}
	pin.Release()
	pin.Release()
	<-g1.Drained()
	g1.Close()
	g1.Close()
	if g1.CloseCount() != 1 || g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("close count=%d lifecycle=%v", g1.CloseCount(), g1.Lifecycle())
	}
}
func TestBlockedPins_AndRetentionBudgetRejectsWithoutKillingOldWork(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewRefGenerationManager(1, runtimehost.NewManualClock(time.Unix(1_700_000_200, 0)))
	g1 := m.PrepareCandidate("g1")
	mustPublish(t, m, g1)
	kinds := []runtimehost.PinKind{runtimehost.PinSSE, runtimehost.PinAsync, runtimehost.PinProvider}
	pins := make([]*runtimehost.RefPin, 0, len(kinds))
	for _, k := range kinds {
		lease, ok := m.Acquire()
		if !ok {
			t.Fatal("acquire")
		}
		pin, ok := lease.TransferPin(k)
		if !ok {
			t.Fatalf("transfer %v", k)
		}
		pins = append(pins, pin)
	}
	// Budget=1: publish g2 ok (fills retained), then g3 blocked while pins hold g1.
	mustPublish(t, m, m.PrepareCandidate("g2"))
	if g1.Refs() != uint32(len(pins)) {
		t.Fatalf("refs=%d", g1.Refs())
	}
	active := m.Active()
	if err := m.Publish(m.PrepareCandidate("g3")); !errors.Is(err, runtimehost.ErrRetentionBlocked) {
		t.Fatalf("want retention blocked, got %v", err)
	}
	if m.Active() != active {
		t.Fatal("retention block must not swap active")
	}
	select {
	case <-g1.Drained():
		t.Fatal("must not kill old pinned work")
	default:
	}
	for i, p := range pins {
		p.Release()
		if i < len(pins)-1 {
			select {
			case <-g1.Drained():
				t.Fatal("drained early")
			default:
			}
		}
	}
	<-g1.Drained()
	g1.Close()
	m.SweepClosed()
	mustPublish(t, m, m.PrepareCandidate("g3"))
	if m.Active().Label != "g3" {
		t.Fatalf("active=%q", m.Active().Label)
	}
}
func TestPublish_AcquireRace_BarrierRounds(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewRefGenerationManager(8, nil)
	mustPublish(t, m, m.PrepareCandidate("seed"))
	for round := 0; round < 40; round++ {
		readyAcq, readyPub, gate := make(chan struct{}), make(chan struct{}), make(chan struct{})
		result := make(chan int64, 1)
		go func() {
			close(readyAcq)
			<-gate
			lease, ok := m.Acquire()
			if !ok {
				result <- -1
				return
			}
			id := lease.Generation().ID
			lease.Release()
			result <- id
		}()
		cand := m.PrepareCandidate("race")
		go func() {
			close(readyPub)
			<-gate
			_ = m.Publish(cand)
		}()
		<-readyAcq
		<-readyPub
		close(gate)
		id := <-result
		if id < 1 || m.Active() == nil || m.Active().ID < id {
			t.Fatalf("round %d id=%d active=%v", round, id, m.Active())
		}
	}
}
func TestProductionGenerationManager_IntegrationRED(t *testing.T) {
	t.Skip("RED until generation manager implementation")
}
func mustPublish(t *testing.T, m *runtimehost.RefGenerationManager, g *runtimehost.RefGeneration) {
	t.Helper()
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
}
