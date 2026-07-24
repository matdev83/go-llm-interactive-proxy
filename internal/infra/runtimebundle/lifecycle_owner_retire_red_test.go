package runtimebundle_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)

// Task 7.1 cross-layer lifecycle targets live next to the real GenerationBundle
// construction helpers (export_test). Req 8.8 is intentional RED against current
// GenerationBundle/ResourceLedger close-result caches that defeat
// Generation/LifecycleWorker retry.

func TestLifecycleOwner_Retire_RetryableCloseThroughGenerationBundle(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	_ = ledger.AddClose("backend", runtimebundle.PhaseClose, func() error {
		if closes.Add(1) == 1 {
			return errors.New("temp-close")
		}
		return nil
	})
	_ = ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error { return nil })
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareRequestPlane("g1-retry", bundle)
	mustPublishBundle(t, m, g1)
	g2 := m.Prepare("g2-active")
	mustPublishBundle(t, m, g2)

	worker := runtimehost.NewLifecycleWorkerWithPolicy(runtimehost.CleanupPolicy{MaxAttempts: 3})
	err := worker.Retire(context.Background(), g1, bundle)
	if err != nil {
		t.Fatalf("Retire must eventually succeed after retryable close: %v (closes=%d lifecycle=%v)", err, closes.Load(), g1.Lifecycle())
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v want GenClosed after successful retry", g1.Lifecycle())
	}
	if closes.Load() != 2 {
		t.Fatalf("resource close executions=%d want 2 (fail then success); cached wrapper made retry a no-op", closes.Load())
	}
	st := worker.LastStatus()
	if st.Outcome != runtimehost.LifecycleOutcomeOK {
		t.Fatalf("status=%+v want OK", st)
	}
	if m.Active() != g2 || g2.Lifecycle() != runtimehost.GenActive {
		t.Fatal("active generation must remain healthy")
	}
}

func TestLifecycleOwner_Retire_StaysClosingAfterRetryableFailureBeforeSuccess(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	ledger := runtimebundle.NewResourceLedger()
	_ = ledger.AddClose("backend", runtimebundle.PhaseClose, func() error {
		n := closes.Add(1)
		if n == 1 {
			startOnce.Do(func() { close(started) })
			<-release
			return errors.New("temp-close")
		}
		return nil
	})
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareRequestPlane("g1", bundle)
	mustPublishBundle(t, m, g1)
	mustPublishBundle(t, m, m.Prepare("g2"))

	<-g1.Drained()
	if err := g1.BeginClose(); err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- g1.Close() }()
	<-started
	if g1.Lifecycle() != runtimehost.GenClosing {
		t.Fatalf("lifecycle=%v want GenClosing during first failing cleanup", g1.Lifecycle())
	}
	close(release)
	if err := <-errCh; err == nil {
		t.Fatal("first Close must fail")
	}
	if g1.Lifecycle() != runtimehost.GenClosing {
		t.Fatalf("lifecycle=%v want GenClosing after retryable failure (req 8.8)", g1.Lifecycle())
	}

	if err := g1.Close(); err != nil {
		t.Fatalf("owned retry Close: %v (closes=%d)", err, closes.Load())
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v want GenClosed", g1.Lifecycle())
	}
	if closes.Load() != 2 {
		t.Fatalf("closes=%d want 2", closes.Load())
	}
}

func TestLifecycleOwner_Retire_PanicIsolatedThroughLifecycleWorker(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	_ = ledger.AddClose("boom", runtimebundle.PhaseClose, func() error {
		if closes.Add(1) == 1 {
			panic("cleanup boom")
		}
		return nil
	})
	_ = ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error { return nil })
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareRequestPlane("g1-panic", bundle)
	mustPublishBundle(t, m, g1)
	mustPublishBundle(t, m, m.Prepare("g2"))

	// LifecycleWorker.safeClose is the preferred isolation boundary for
	// Generation→owned cleanup panics (req 8.8 / 8.10). Ledger may also convert
	// panics to errors; either boundary must keep GenClosing retryable.
	worker := runtimehost.NewLifecycleWorkerWithPolicy(runtimehost.CleanupPolicy{MaxAttempts: 3})
	err := worker.Retire(context.Background(), g1, bundle)
	if err != nil {
		t.Fatalf("Retire after panic isolation must succeed: %v (closes=%d lifecycle=%v)", err, closes.Load(), g1.Lifecycle())
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v want GenClosed", g1.Lifecycle())
	}
	if closes.Load() != 2 {
		t.Fatalf("closes=%d want 2 (panic fail then retry success); permanent claim defeated retry", closes.Load())
	}
}

func TestLifecycleOwner_Discard_UnpublishedGenerationBundleRollbackOnce(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	_ = ledger.AddClose("backend", runtimebundle.PhaseClose, func() error {
		closes.Add(1)
		return nil
	})
	_ = ledger.AddClose("prepare", runtimebundle.PhasePrepare, func() error {
		closes.Add(1)
		return nil
	})
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	m := runtimehost.NewManager(2, nil)
	g := m.PrepareRequestPlane("unpublished", bundle)
	if g.Lifecycle() != runtimehost.GenPrepared {
		t.Fatalf("lifecycle=%v want GenPrepared before Discard", g.Lifecycle())
	}
	if err := g.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if closes.Load() != 2 {
		t.Fatalf("Discard cleanup executions=%d want 2 (close+prepare phases)", closes.Load())
	}
	if err := g.Discard(); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("second Discard: %v", err)
	}
	if closes.Load() != 2 {
		t.Fatalf("repeated Discard double-cleaned closes=%d", closes.Load())
	}
}

func TestLifecycleOwner_Retire_ConcurrentRetirementNoDoubleClean(t *testing.T) {
	t.Parallel()
	var quiesces, closes atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	_ = ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error {
		quiesces.Add(1)
		return nil
	})
	_ = ledger.AddClose("backend", runtimebundle.PhaseClose, func() error {
		closes.Add(1)
		return nil
	})
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	m := runtimehost.NewManager(4, nil)
	g1 := m.PrepareRequestPlane("g1", bundle)
	mustPublishBundle(t, m, g1)
	mustPublishBundle(t, m, m.Prepare("g2"))

	worker := runtimehost.NewLifecycleWorker()
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- worker.Retire(context.Background(), g1, bundle)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var sawOK int
	for err := range errs {
		switch {
		case err == nil:
			sawOK++
		case errors.Is(err, runtimehost.ErrAlreadyClosed):
		default:
			t.Fatalf("unexpected retire err: %v", err)
		}
	}
	if sawOK < 1 {
		t.Fatal("expected at least one successful Retire")
	}
	if quiesces.Load() != 1 {
		t.Fatalf("quiesce executions=%d want 1", quiesces.Load())
	}
	if closes.Load() != 1 {
		t.Fatalf("close executions=%d want 1", closes.Load())
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v", g1.Lifecycle())
	}
}

func TestLifecycleOwner_Close_RepeatedShutdownCannotDoubleClean(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	_ = ledger.AddClose("backend", runtimebundle.PhaseClose, func() error {
		closes.Add(1)
		return nil
	})
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareRequestPlane("g1", bundle)
	mustPublishBundle(t, m, g1)
	mustPublishBundle(t, m, m.Prepare("g2"))

	worker := runtimehost.NewLifecycleWorker()
	if err := worker.Retire(context.Background(), g1, bundle); err != nil {
		t.Fatal(err)
	}
	if err := worker.Retire(context.Background(), g1, bundle); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("second Retire: %v", err)
	}
	if err := g1.Close(); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("Close after closed: %v", err)
	}
	if closes.Load() != 1 {
		t.Fatalf("closes=%d want 1", closes.Load())
	}
}

// TestLifecycleOwner_ShutdownDetached_RacesRetirementNoDoubleCleanOrProcessClose
// races Manager.ShutdownDetached with LifecycleWorker.Retire against a real
// GenerationBundle/ResourceLedger. Proves no generation-resource double cleanup
// and no process-owned premature close (channels/atomics + context bound; no sleep).
func TestLifecycleOwner_ShutdownDetached_RacesRetirementNoDoubleCleanOrProcessClose(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	var closes atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	_ = ledger.AddClose("backend", runtimebundle.PhaseClose, func() error {
		closes.Add(1)
		return nil
	})
	_ = ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error {
		return nil
	})
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	m := runtimehost.NewManager(4, nil)
	g1 := m.PrepareRequestPlane("g1-shutdown", bundle)
	mustPublishBundle(t, m, g1)
	g2 := m.Prepare("g2-keep")
	mustPublishBundle(t, m, g2)

	worker := runtimehost.NewLifecycleWorker()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 4)

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		errs <- worker.Retire(ctx, g1, bundle)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		errs <- m.ShutdownDetached(ctx, worker)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		errs <- worker.Retire(ctx, g1, bundle)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		errs <- m.ShutdownDetached(ctx, worker)
	}()
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err == nil || errors.Is(err, runtimehost.ErrAlreadyClosed) {
			continue
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown/retire hit context bound unexpectedly: %v", err)
		}
		t.Fatalf("unexpected shutdown/retire err: %v", err)
	}
	if closes.Load() != 1 {
		t.Fatalf("generation resource closes=%d want 1 (no double cleanup)", closes.Load())
	}
	if ps.Closed() {
		t.Fatal("process-owned resources must not close during generation retirement/shutdown race")
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("g1 lifecycle=%v want GenClosed", g1.Lifecycle())
	}
}
