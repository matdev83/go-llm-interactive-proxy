package billing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type CallRatingResolver interface {
	ResolveCallRating(context.Context, CompleteCall, CallExposure) (CallRatingResult, error)
}
type CallPostUsageWorker struct {
	usage      CallUsageStore
	settlement CallSettlementStore
	resolver   CallRatingResolver
	batch      int
	interval   time.Duration
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
}

func NewCallPostUsageWorker(usage CallUsageStore, settlement CallSettlementStore, resolver CallRatingResolver, batch int) (*CallPostUsageWorker, error) {
	if usage == nil || settlement == nil || resolver == nil {
		return nil, errors.New("billing: complete-call worker dependencies are required")
	}
	if batch <= 0 {
		batch = 32
	}
	return &CallPostUsageWorker{usage: usage, settlement: settlement, resolver: resolver, batch: batch, interval: time.Second}, nil
}

func (w *CallPostUsageWorker) Start(ctx context.Context) error {
	if w == nil {
		return errors.New("billing: nil complete-call worker")
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

func (w *CallPostUsageWorker) Stop(ctx context.Context) error {
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

func (w *CallPostUsageWorker) ProcessOnce(ctx context.Context) error {
	if w == nil || w.usage == nil || w.settlement == nil || w.resolver == nil {
		return errors.New("billing: incomplete complete-call worker")
	}
	calls, err := w.usage.ClaimCompleteCalls(ctx, w.batch)
	if err != nil {
		return fmt.Errorf("billing: claim complete calls: %w", err)
	}
	var allErr error
	for _, complete := range calls {
		exposure, err := w.usage.GetCallExposure(ctx, complete.Closure.CallID)
		if err != nil {
			allErr = errors.Join(allErr, w.retryCall(ctx, complete.Closure.CallID, "exposure_lookup", err))
			continue
		}
		result, err := w.resolver.ResolveCallRating(ctx, complete, exposure)
		if err != nil {
			code := "rating_input"
			if errors.Is(err, ErrBillingAttemptSequenceUnknown) {
				code = "settlement_reconcile_required"
			}
			allErr = errors.Join(allErr, w.retryCall(ctx, complete.Closure.CallID, code, err))
			continue
		}
		if _, err := w.settlement.ApplyCallBillingResult(ctx, ApplyCallBillingInput{Call: complete.Closure, Exposure: exposure, Result: result}); err != nil {
			code := "settlement"
			if errors.Is(err, ErrSettlementReconcileRequired) {
				code = "settlement_reconcile_required"
			}
			allErr = errors.Join(allErr, w.retryCall(ctx, complete.Closure.CallID, code, err))
		}
	}
	return allErr
}

func (w *CallPostUsageWorker) retryCall(ctx context.Context, callID BillingCallID, code string, cause error) error {
	if retryer, ok := w.usage.(interface {
		RetryCompleteCall(context.Context, BillingCallID, string) error
	}); ok {
		if err := retryer.RetryCompleteCall(ctx, callID, code); err != nil {
			return errors.Join(fmt.Errorf("billing: %s: %w", code, cause), err)
		}
	}
	return fmt.Errorf("billing: %s: %w", code, cause)
}
