package billing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type CallProviderCostWorker struct {
	work     ProviderCostWorkReader
	store    ProviderCostStore
	resolver ProviderCostResolver
	batch    int
	interval time.Duration
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
}

func NewCallProviderCostWorker(work ProviderCostWorkReader, store ProviderCostStore, resolver ProviderCostResolver, batch int) (*CallProviderCostWorker, error) {
	if work == nil || store == nil || resolver == nil {
		return nil, errors.New("billing: durable provider-cost work, store, and resolver are required")
	}
	if batch <= 0 {
		batch = 32
	}
	return &CallProviderCostWorker{work: work, store: store, resolver: resolver, batch: batch, interval: time.Second}, nil
}

func (w *CallProviderCostWorker) Start(ctx context.Context) error {
	if w == nil {
		return errors.New("billing: nil provider-cost worker")
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

func (w *CallProviderCostWorker) Stop(ctx context.Context) error {
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

func (w *CallProviderCostWorker) ProcessOnce(ctx context.Context) error {
	if w == nil || w.work == nil || w.store == nil || w.resolver == nil {
		return errors.New("billing: incomplete provider-cost worker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	work, err := w.work.ListPendingProviderCostWork(ctx, w.batch)
	if err != nil {
		return fmt.Errorf("billing: list pending provider-cost work: %w", err)
	}
	return w.processWork(ctx, work)
}

func (w *CallProviderCostWorker) processWork(ctx context.Context, work []ProviderCostWork) error {
	failureStore, hasFailureStore := w.store.(ProviderCostWorkFailureStore)
	var allErr error
	for _, item := range work {
		if item.AccountID == "" {
			err := ErrProviderCostCallUnavailable
			if hasFailureStore {
				err = errors.Join(err, failureStore.DeferProviderCostWork(ctx, item, err.Error()))
			}
			allErr = errors.Join(allErr, err)
			continue
		}
		result, err := w.resolver.ResolveProviderCost(ctx, item.Leg)
		if err != nil {
			if failures, ok := w.store.(ProviderCostFailureStore); ok {
				markerErr := failures.MarkProviderCostUnreconciled(ctx, ApplyProviderCostInput{AccountID: item.AccountID, CallID: item.CallID, Leg: item.Leg}, err.Error())
				allErr = errors.Join(allErr, err, markerErr)
			}
			if hasFailureStore {
				allErr = errors.Join(allErr, failureStore.DeferProviderCostWork(ctx, item, err.Error()))
			} else {
				allErr = errors.Join(allErr, err)
			}
			continue
		}
		if _, err := w.store.ApplyProviderCost(ctx, ApplyProviderCostInput{AccountID: item.AccountID, CallID: item.CallID, Leg: item.Leg, Result: result}); err != nil {
			if hasFailureStore {
				allErr = errors.Join(allErr, failureStore.DeferProviderCostWork(ctx, item, err.Error()))
			} else {
				allErr = errors.Join(allErr, err)
			}
		}
	}
	return allErr
}
