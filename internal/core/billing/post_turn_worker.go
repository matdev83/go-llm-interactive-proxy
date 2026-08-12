package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// PostTurnStore is the durable post-turn boundary. It deliberately contains
// processing metadata and settlement, but no stream or provider concerns.
type PostTurnStore interface {
	ProcessingStore
	SettlementStore
}

// ProcessingStore owns mutable claim/retry metadata for sealed usage records.
type ProcessingStore interface {
	ClaimPending(context.Context, int) ([]TurnUsageRecord, error)
	MarkProcessingRetryable(context.Context, string, string, string) error
	MarkProcessingTerminal(context.Context, string, string, string) error
	MarkProcessingUnreconciledCost(context.Context, string, string, string) error
	MarkProcessingProcessed(context.Context, string, string, string) error
	// MarkProcessingInvariantFailure marks the TUR terminal and atomically
	// transitions the account to reconcile_required. The hold stays open.
	MarkProcessingInvariantFailure(context.Context, TurnUsageRecord, string) error
}

// RatingResolver resolves the exact authorization and immutable pricing/policy/
// operator-rate snapshots bound to one sealed TUR. Snapshot lookup belongs at the
// composition root; the worker never reads provider or database types.
type RatingResolver interface {
	ResolveRating(context.Context, TurnUsageRecord) (RatingInput, error)
}

// PostTurnWorkerConfig controls bounded post-turn polling.
type PostTurnWorkerConfig struct {
	BatchSize int
	Interval  time.Duration
	Log       *slog.Logger
}

// PostTurnWorker claims sealed TURs and performs deterministic rating followed by
// atomic settlement. Runtime output and execution state are never consulted.
type PostTurnWorker struct {
	store    PostTurnStore
	resolver RatingResolver
	batch    int
	interval time.Duration
	log      *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewPostTurnWorker(store PostTurnStore, resolver RatingResolver, cfg PostTurnWorkerConfig) (*PostTurnWorker, error) {
	if store == nil {
		return nil, errors.New("billing: post-turn store is required")
	}
	if resolver == nil {
		return nil, errors.New("billing: post-turn rating resolver is required")
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 32
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	return &PostTurnWorker{store: store, resolver: resolver, batch: cfg.BatchSize, interval: cfg.Interval, log: cfg.Log}, nil
}

// Start launches the worker exactly once. Authoritative billing starts it after
// the generation request plane is published. Generation retirement stops it
// before generation-owned resources close.
func (w *PostTurnWorker) Start(ctx context.Context) error {
	if w == nil {
		return errors.New("billing: nil post-turn worker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done != nil {
		return nil
	}
	workerCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.done = make(chan struct{})
	done := w.done
	go func() {
		defer close(done)
		w.loop(workerCtx)
	}()
	return nil
}

// Stop cancels the worker and waits for its current bounded batch to finish.
func (w *PostTurnWorker) Stop(ctx context.Context) error {
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

// ProcessOnce claims and processes one bounded batch. It is exported for
// deterministic startup/repair tests and operator-driven draining.
func (w *PostTurnWorker) ProcessOnce(ctx context.Context) error {
	if w == nil || w.store == nil || w.resolver == nil {
		return errors.New("billing: incomplete post-turn worker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if reclaimer, ok := w.store.(interface {
		ReclaimExpiredHolds(context.Context, int) (int, error)
	}); ok {
		// Store reclaim is intentionally a no-op without Req 15.6 stale-safe
		// proof; the call remains so composition does not special-case the port.
		if _, err := reclaimer.ReclaimExpiredHolds(ctx, w.batch); err != nil && w.log != nil {
			w.log.DebugContext(ctx, "billing expired-hold reclaim", "error", err)
		}
	}
	records, err := w.store.ClaimPending(ctx, w.batch)
	if err != nil {
		return fmt.Errorf("billing: claim post-turn records: %w", err)
	}
	var allErr error
	for _, record := range records {
		if err := w.processRecord(ctx, record); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	return allErr
}

func (w *PostTurnWorker) processRecord(ctx context.Context, record TurnUsageRecord) error {
	input, err := w.resolver.ResolveRating(ctx, record)
	if err != nil {
		return w.retry(ctx, record, "rating_input_unavailable", err)
	}
	result, err := RateTurnAndMarkProcessing(ctx, w.store, input)
	if err != nil {
		return w.failProcessing(ctx, record, "rating_failed", err)
	}
	if result.UnreconciledCost {
		return nil
	}
	if _, err := w.store.ApplyBillingResult(ctx, ApplyBillingInput{Record: record, Authorization: input.Authorization, Result: result}); err != nil {
		return w.failProcessing(ctx, record, "settlement_failed", err)
	}
	// ApplyBillingResult owns the atomic processed-state transition together with
	// journal postings and hold close. The worker must not update it separately.
	return nil
}

func (w *PostTurnWorker) failProcessing(ctx context.Context, record TurnUsageRecord, code string, cause error) error {
	wrapped := fmt.Errorf("billing: %s for %s: %w", code, record.Key, cause)
	if isAccountIntegrityFailure(cause) {
		if err := w.store.MarkProcessingInvariantFailure(ctx, record, "billing_invariant"); err != nil {
			return errors.Join(wrapped, err)
		}
		return wrapped
	}
	if isPermanentPostTurnError(cause) {
		if err := w.store.MarkProcessingTerminal(ctx, record.Key, record.Fingerprint, "billing_invariant"); err != nil {
			return errors.Join(wrapped, err)
		}
		return wrapped
	}
	return w.retry(ctx, record, code, cause)
}

func (w *PostTurnWorker) retry(ctx context.Context, record TurnUsageRecord, code string, cause error) error {
	if err := w.store.MarkProcessingRetryable(ctx, record.Key, record.Fingerprint, code); err != nil {
		return errors.Join(fmt.Errorf("billing: %s for %s: %w", code, record.Key, cause), err)
	}
	return fmt.Errorf("billing: %s for %s: %w", code, record.Key, cause)
}

func isPermanentPostTurnError(err error) bool {
	return isAccountIntegrityFailure(err) ||
		errors.Is(err, ErrInvalidRecord) ||
		errors.Is(err, ErrRatingInvalid) ||
		errors.Is(err, ErrRatingSnapshotMismatch) ||
		errors.Is(err, ErrRatingCurrencyMismatch) ||
		errors.Is(err, ErrRatingEvidenceMissing) ||
		errors.Is(err, ErrSettlementInvalid) ||
		errors.Is(err, ErrEstimateOverflow) ||
		// Spendable failures cannot self-heal by reclaiming the same TUR.
		// ErrAccountNotReady is retryable: in-flight holds must settle after a
		// verified rebuild rather than being terminalized and released.
		errors.Is(err, ErrInsufficientSpendable)
}

func isAccountIntegrityFailure(err error) bool {
	return errors.Is(err, ErrActualChargeExceedsAuthorization) ||
		errors.Is(err, ErrSettlementConflict) ||
		errors.Is(err, ErrProcessingConflict) ||
		errors.Is(err, ErrJournalInvalid) ||
		errors.Is(err, ErrJournalUnbalanced) ||
		errors.Is(err, ErrJournalFingerprint)
}

func (w *PostTurnWorker) loop(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		if err := w.ProcessOnce(ctx); err != nil && w.log != nil && !errors.Is(err, context.Canceled) {
			w.log.DebugContext(ctx, "billing post-turn batch", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Compile-time documentation of the narrow processing contract.
var _ ProcessingMarker = PostTurnStore(nil)
