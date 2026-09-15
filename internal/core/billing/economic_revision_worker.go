package billing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

const (
	defaultEconomicRevisionBatchSize = 32
	economicRevisionClaimLease       = 30 * time.Second
	economicRevisionStateReleaseWait = 5 * time.Second
)

var economicRevisionWorkerSequence atomic.Uint64

// EconomicRevisionWorker performs bounded pure valuation/reconciliation over
// durable revisions. It deliberately has no settlement or balance-store
// dependency; those effects belong to a later policy-owned transition.
type EconomicRevisionWorker struct {
	work       EconomicRevisionWorkReader
	results    EconomicRevisionResultStore
	rater      PostUsageRater
	reconciler EconomicRevisionReconciler
	queue      EconomicQueue
	batch      int
	interval   time.Duration
	owner      string
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
}

// NewEconomicRevisionWorker constructs a pure worker for one independent
// customer or provider queue.
func NewEconomicRevisionWorker(work EconomicRevisionWorkReader, results EconomicRevisionResultStore, rater PostUsageRater, queue EconomicQueue, batch int) (*EconomicRevisionWorker, error) {
	return newEconomicRevisionWorker(work, results, rater, nil, queue, batch)
}

// NewEconomicRevisionWorkerWithReconciler adds an optional pure reconciliation
// calculation to the valuation worker. Reconciliation output is persisted in
// the same local transaction as its valuation and head transition.
func NewEconomicRevisionWorkerWithReconciler(work EconomicRevisionWorkReader, results EconomicRevisionResultStore, rater PostUsageRater, reconciler EconomicRevisionReconciler, queue EconomicQueue, batch int) (*EconomicRevisionWorker, error) {
	return newEconomicRevisionWorker(work, results, rater, reconciler, queue, batch)
}

func newEconomicRevisionWorker(work EconomicRevisionWorkReader, results EconomicRevisionResultStore, rater PostUsageRater, reconciler EconomicRevisionReconciler, queue EconomicQueue, batch int) (*EconomicRevisionWorker, error) {
	if work == nil || results == nil || rater == nil {
		return nil, errors.New("billing: economic revision queue, result store, and rater are required")
	}
	if err := queue.Validate(); err != nil {
		return nil, err
	}
	if batch <= 0 {
		batch = defaultEconomicRevisionBatchSize
	}
	return &EconomicRevisionWorker{
		work: work, results: results, rater: rater, reconciler: reconciler,
		queue: queue, batch: batch, interval: time.Second,
		owner: fmt.Sprintf("economic-revision-worker-%d", economicRevisionWorkerSequence.Add(1)),
	}, nil
}

// Start runs bounded polling until the supplied context is canceled.
func (w *EconomicRevisionWorker) Start(ctx context.Context) error {
	if w == nil {
		return errors.New("billing: nil economic revision worker")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done != nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	workerCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.done = make(chan struct{})
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			_ = w.ProcessOnce(workerCtx)
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

// Stop cancels polling and waits for the worker goroutine to leave.
func (w *EconomicRevisionWorker) Stop(ctx context.Context) error {
	if w == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if done == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ProcessOnce claims a bounded queue snapshot and computes each revision. A
// durable result probe is used when available so restart/replay does not call
// the rater again for an already persisted immutable result.
func (w *EconomicRevisionWorker) ProcessOnce(ctx context.Context) error {
	if w == nil || w.work == nil || w.results == nil || w.rater == nil {
		return errors.New("billing: incomplete economic revision worker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	items, err := w.work.ListPendingEconomicRevisionWork(ctx, w.queue, w.batch)
	if err != nil {
		return fmt.Errorf("billing: list %s economic revisions: %w", w.queue, err)
	}
	var allErr error
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return errors.Join(allErr, err)
		}
		if err := w.processRevision(ctx, item); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	return allErr
}

func (w *EconomicRevisionWorker) processRevision(ctx context.Context, item EconomicRevisionWork) error {
	work, err := item.Normalize()
	if err != nil {
		return fmt.Errorf("billing: normalize %s economic revision: %w", w.queue, err)
	}
	identity, err := work.Identity()
	if err != nil {
		return fmt.Errorf("billing: identify %s economic revision: %w", w.queue, err)
	}
	stateStore, hasState := w.work.(EconomicRevisionWorkStateStore)
	var claim EconomicRevisionWorkClaim
	if hasState {
		var claimed bool
		claim, claimed, err = stateStore.ClaimEconomicRevisionWork(ctx, work, w.owner, economicRevisionClaimLease)
		if err != nil {
			return fmt.Errorf("billing: claim %s economic revision %s: %w", w.queue, identity.Key(), err)
		}
		if !claimed {
			return nil
		}
	}

	retry := func(cause error) error {
		if !hasState {
			return cause
		}
		retryCtx, cancel := economicRevisionStateContext(ctx)
		defer cancel()
		retryErr := stateStore.RetryEconomicRevisionWork(retryCtx, work, claim, cause.Error(), time.Now().UTC())
		if retryErr != nil {
			return errors.Join(cause, fmt.Errorf("billing: retry %s economic revision %s: %w", w.queue, identity.Key(), retryErr))
		}
		return cause
	}
	complete := func() error {
		if !hasState {
			return nil
		}
		completeCtx, cancel := economicRevisionStateContext(ctx)
		defer cancel()
		if err := stateStore.CompleteEconomicRevisionWork(completeCtx, work, claim); err != nil {
			return fmt.Errorf("billing: complete %s economic revision %s: %w", w.queue, identity.Key(), err)
		}
		return nil
	}

	if probe, ok := w.results.(EconomicRevisionResultProbe); ok {
		processed, probeErr := probe.HasEconomicRevisionResult(ctx, identity)
		if probeErr != nil {
			return retry(fmt.Errorf("billing: probe %s economic revision: %w", w.queue, probeErr))
		}
		if processed {
			return complete()
		}
	}

	valuation, err := w.rater.Rate(ctx, work.Input.Clone())
	if err != nil {
		return retry(fmt.Errorf("billing: rate %s economic revision %s: %w", w.queue, identity.Key(), err))
	}
	valuation, err = normalizeRevisionValuation(work, identity, valuation)
	if err != nil {
		return retry(fmt.Errorf("billing: normalize valuation %s: %w", identity.Key(), err))
	}
	var reconciliation *EconomicReconciliation
	if w.reconciler != nil {
		reconciliation, err = w.reconciler.Reconcile(ctx, work, valuation)
		if err != nil {
			return retry(fmt.Errorf("billing: reconcile economic revision %s: %w", identity.Key(), err))
		}
		if reconciliation == nil {
			return retry(fmt.Errorf("billing: reconcile economic revision %s: nil result", identity.Key()))
		}
		reconciliation, err = normalizeRevisionReconciliation(work, identity, *reconciliation)
		if err != nil {
			return retry(fmt.Errorf("billing: normalize reconciliation %s: %w", identity.Key(), err))
		}
	}
	if err := w.results.AppendEconomicRevisionResult(ctx, work, EconomicRevisionResult{Valuation: valuation, Reconciliation: reconciliation}); err != nil {
		return retry(fmt.Errorf("billing: persist %s economic revision %s: %w", w.queue, identity.Key(), err))
	}
	return complete()
}

// economicRevisionStateContext keeps a bounded release attempt possible when
// a rater observes cancellation. Normal database work still honors the caller
// context; only cleanup after cancellation is detached and time-limited.
func economicRevisionStateContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil || ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), economicRevisionStateReleaseWait)
}

func normalizeRevisionValuation(work EconomicRevisionWork, identity EconomicRevisionIdentity, valuation economics.Valuation) (economics.Valuation, error) {
	out := valuation.Clone()
	if !sameSubject(out.Subject, work.Subject) {
		return economics.Valuation{}, fmt.Errorf("%w: valuation subject", ErrEconomicRevisionSubjectMismatch)
	}
	if out.Basis != work.Input.Basis {
		return economics.Valuation{}, fmt.Errorf("%w: got=%q want=%q", ErrEconomicRevisionBasisMismatch, out.Basis, work.Input.Basis)
	}
	computedHash, err := economics.CanonicalInputSetHash(out.Basis, out.InputObservations)
	if err != nil {
		return economics.Valuation{}, fmt.Errorf("%w: output references: %v", ErrEconomicRevisionInputMismatch, err)
	}
	if out.InputSetHash != "" && out.InputSetHash != computedHash {
		return economics.Valuation{}, &EconomicRevisionInputMismatchError{Expected: computedHash, Actual: out.InputSetHash}
	}
	if computedHash != work.InputSetHash {
		return economics.Valuation{}, &EconomicRevisionInputMismatchError{Expected: work.InputSetHash, Actual: computedHash}
	}
	out.InputSetHash = computedHash
	out.ID = identity.ValuationKey()
	out.Version = economics.ValuationVersionV2
	// CreatedAt is derived processing metadata, not rater-authored evidence.
	// Anchor it to the immutable work envelope so retries cannot vary the
	// valuation fingerprint with the rater's wall clock.
	out.CreatedAt = work.CreatedAt
	if err := out.Validate(); err != nil {
		return economics.Valuation{}, fmt.Errorf("%w: valuation: %v", ErrEconomicRevisionInputMismatch, err)
	}
	return out, nil
}

func normalizeRevisionReconciliation(work EconomicRevisionWork, identity EconomicRevisionIdentity, reconciliation EconomicReconciliation) (*EconomicReconciliation, error) {
	out := reconciliation
	if !sameSubject(out.Subject, work.Subject) {
		return nil, fmt.Errorf("%w: reconciliation subject", ErrEconomicRevisionSubjectMismatch)
	}
	if out.Basis != work.Input.Basis {
		return nil, fmt.Errorf("%w: reconciliation got=%q want=%q", ErrEconomicRevisionBasisMismatch, out.Basis, work.Input.Basis)
	}
	if out.InputSetHash != "" && out.InputSetHash != work.InputSetHash {
		return nil, fmt.Errorf("%w: reconciliation hash supplied=%q expected=%q", ErrEconomicRevisionInputMismatch, out.InputSetHash, work.InputSetHash)
	}
	out.ID = identity.ReconciliationKey()
	out.Version = work.EvidenceRevision
	out.InputSetHash = work.InputSetHash
	// Reconciliation CreatedAt is likewise derived processing metadata. The
	// immutable work timestamp keeps duplicate workers byte/fingerprint-stable.
	out.CreatedAt = work.CreatedAt
	normalized, err := out.Normalize()
	if err != nil {
		return nil, err
	}
	return &normalized, nil
}
