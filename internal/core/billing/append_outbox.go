package billing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrUsageAppendOutboxEnqueue = errors.New("billing: usage append outbox enqueue failed")

type UsageAppendKind string

const (
	UsageAppendCall UsageAppendKind = "call"
	UsageAppendLeg  UsageAppendKind = "leg"
)

type UsageAppendWork struct {
	Key  string
	Kind UsageAppendKind
	Call *CallUsageRecord
	Leg  *CallLegUsageRecord
}
type UsageAppendOutbox interface {
	EnqueueCallUsageAppend(context.Context, CallUsageRecord, string) error
	EnqueueCallLegUsageAppend(context.Context, CallLegUsageRecord, string) error
	ListPendingUsageAppendWork(context.Context, int) ([]UsageAppendWork, error)
	MarkUsageAppendProcessed(context.Context, string) error
	DeferUsageAppend(context.Context, string, string) error
	FailUsageAppend(context.Context, string, string) error
}
type RetryingCallUsageAppender struct {
	primary CallUsageAppender
	outbox  UsageAppendOutbox
}

func NewRetryingCallUsageAppender(primary CallUsageAppender, outbox UsageAppendOutbox) (*RetryingCallUsageAppender, error) {
	if primary == nil || outbox == nil {
		return nil, errors.New("billing: call usage appender and durable outbox are required")
	}
	return &RetryingCallUsageAppender{primary: primary, outbox: outbox}, nil
}

func (a *RetryingCallUsageAppender) AppendCallUsage(ctx context.Context, record CallUsageRecord) error {
	if a == nil || a.primary == nil || a.outbox == nil {
		return errors.New("billing: incomplete retrying call usage appender")
	}
	if err := a.primary.AppendCallUsage(ctx, record); err != nil {
		if errors.Is(err, ErrReplayConflict) {
			return err
		}
		enqueueErr := enqueueCallUsageAfterFailure(ctx, a.outbox, record, err)
		return errors.Join(err, enqueueErr)
	}
	return nil
}

type RetryingCallLegUsageAppender struct {
	primary CallLegUsageAppender
	outbox  UsageAppendOutbox
}

func NewRetryingCallLegUsageAppender(primary CallLegUsageAppender, outbox UsageAppendOutbox) (*RetryingCallLegUsageAppender, error) {
	if primary == nil || outbox == nil {
		return nil, errors.New("billing: call-leg usage appender and durable outbox are required")
	}
	return &RetryingCallLegUsageAppender{primary: primary, outbox: outbox}, nil
}

func (a *RetryingCallLegUsageAppender) AppendCallLegUsage(ctx context.Context, record CallLegUsageRecord) error {
	if a == nil || a.primary == nil || a.outbox == nil {
		return errors.New("billing: incomplete retrying call-leg usage appender")
	}
	if err := a.primary.AppendCallLegUsage(ctx, record); err != nil {
		if errors.Is(err, ErrReplayConflict) {
			return err
		}
		enqueueErr := enqueueCallLegAfterFailure(ctx, a.outbox, record, err)
		return errors.Join(err, enqueueErr)
	}
	return nil
}

const usageAppendOutboxEnqueueTimeout = 30 * time.Second

func enqueueContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), usageAppendOutboxEnqueueTimeout)
}

func enqueueCallUsageAfterFailure(ctx context.Context, outbox UsageAppendOutbox, record CallUsageRecord, appendErr error) error {
	enqueueCtx, cancel := enqueueContext(ctx)
	defer cancel()
	if err := outbox.EnqueueCallUsageAppend(enqueueCtx, record, appendErr.Error()); err != nil {
		return fmt.Errorf("%w: %w", ErrUsageAppendOutboxEnqueue, err)
	}
	return nil
}

func enqueueCallLegAfterFailure(ctx context.Context, outbox UsageAppendOutbox, record CallLegUsageRecord, appendErr error) error {
	enqueueCtx, cancel := enqueueContext(ctx)
	defer cancel()
	if err := outbox.EnqueueCallLegUsageAppend(enqueueCtx, record, appendErr.Error()); err != nil {
		return fmt.Errorf("%w: %w", ErrUsageAppendOutboxEnqueue, err)
	}
	return nil
}

type UsageAppendWorker struct {
	outbox UsageAppendOutbox
	calls  CallUsageAppender
	legs   CallLegUsageAppender
	batch  int
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewUsageAppendWorker(outbox UsageAppendOutbox, calls CallUsageAppender, legs CallLegUsageAppender, batch int) (*UsageAppendWorker, error) {
	if outbox == nil || calls == nil || legs == nil {
		return nil, errors.New("billing: durable append outbox and both appenders are required")
	}
	if batch <= 0 {
		batch = 32
	}
	return &UsageAppendWorker{outbox: outbox, calls: calls, legs: legs, batch: batch}, nil
}

func (w *UsageAppendWorker) Start(ctx context.Context) error {
	if w == nil {
		return errors.New("billing: nil usage append worker")
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
		ticker := time.NewTicker(time.Second)
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

func (w *UsageAppendWorker) Stop(ctx context.Context) error {
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

func (w *UsageAppendWorker) ProcessOnce(ctx context.Context) error {
	if w == nil || w.outbox == nil || w.calls == nil || w.legs == nil {
		return errors.New("billing: incomplete usage append worker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	work, err := w.outbox.ListPendingUsageAppendWork(ctx, w.batch)
	if err != nil {
		return fmt.Errorf("billing: list pending usage appends: %w", err)
	}
	var allErr error
	for _, item := range work {
		appendErr := w.replay(ctx, item)
		if appendErr == nil {
			if err := w.outbox.MarkUsageAppendProcessed(ctx, item.Key); err != nil {
				allErr = errors.Join(allErr, fmt.Errorf("billing: mark usage append %s processed: %w", item.Key, err))
			}
			continue
		}
		if errors.Is(appendErr, ErrReplayConflict) {
			if err := w.outbox.FailUsageAppend(ctx, item.Key, appendErr.Error()); err != nil {
				allErr = errors.Join(allErr, appendErr, fmt.Errorf("billing: fail usage append %s: %w", item.Key, err))
			} else {
				allErr = errors.Join(allErr, appendErr)
			}
			continue
		}
		if err := w.outbox.DeferUsageAppend(ctx, item.Key, appendErr.Error()); err != nil {
			allErr = errors.Join(allErr, appendErr, fmt.Errorf("billing: defer usage append %s: %w", item.Key, err))
		} else {
			allErr = errors.Join(allErr, appendErr)
		}
	}
	return allErr
}

func (w *UsageAppendWorker) replay(ctx context.Context, item UsageAppendWork) error {
	switch item.Kind {
	case UsageAppendCall:
		if item.Call == nil {
			return errors.New("billing: usage append work has nil call payload")
		}
		return w.calls.AppendCallUsage(ctx, *item.Call)
	case UsageAppendLeg:
		if item.Leg == nil {
			return errors.New("billing: usage append work has nil leg payload")
		}
		return w.legs.AppendCallLegUsage(ctx, *item.Leg)
	default:
		return fmt.Errorf("billing: unsupported usage append kind %q", item.Kind)
	}
}
