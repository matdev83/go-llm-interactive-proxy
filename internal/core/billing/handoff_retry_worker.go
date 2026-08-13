package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// HandoffRetryConfig bounds detached TUR seal retries. Evidence retries stay
// unlimited by default so provider-accepted usage is never dropped in-process.
type HandoffRetryConfig struct {
	NoEvidenceMaxAttempts int
	EvidenceMaxAttempts   int
	RetryDelay            time.Duration
	MaxRetryDelay         time.Duration
	WriteTimeout          time.Duration
	Log                   *slog.Logger
}

const (
	DefaultHandoffNoEvidenceMaxAttempts = 10
	DefaultHandoffRetryDelay            = 100 * time.Millisecond
	DefaultHandoffMaxRetryDelay         = 5 * time.Second
	DefaultHandoffWriteTimeout          = 2 * time.Second
	DefaultHandoffCloseWait             = 10 * time.Second
)

func (c HandoffRetryConfig) normalized() HandoffRetryConfig {
	if c.NoEvidenceMaxAttempts <= 0 {
		c.NoEvidenceMaxAttempts = DefaultHandoffNoEvidenceMaxAttempts
	}
	if c.RetryDelay <= 0 {
		c.RetryDelay = DefaultHandoffRetryDelay
	}
	if c.MaxRetryDelay <= 0 {
		c.MaxRetryDelay = DefaultHandoffMaxRetryDelay
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = DefaultHandoffWriteTimeout
	}
	return c
}

// HandoffRetryWorker claims pending TUR handoff jobs and appends sealed records.
// It is the only retry owner; runtime streams never hold this loop.
type HandoffRetryWorker struct {
	outbox  HandoffOutbox
	append  UsageRecordAppender
	release HoldReleaser
	cfg     HandoffRetryConfig

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewHandoffRetryWorker(outbox HandoffOutbox, appender UsageRecordAppender, releaser HoldReleaser, cfg HandoffRetryConfig) (*HandoffRetryWorker, error) {
	if outbox == nil {
		return nil, errors.New("billing: handoff outbox is required")
	}
	return &HandoffRetryWorker{outbox: outbox, append: appender, release: releaser, cfg: cfg.normalized()}, nil
}

func (w *HandoffRetryWorker) ReplaceConfig(cfg HandoffRetryConfig) {
	if w == nil {
		return
	}
	w.cfg = cfg.normalized()
}

func (w *HandoffRetryWorker) Start(ctx context.Context) error {
	if w == nil {
		return errors.New("billing: nil handoff retry worker")
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
		ticker := time.NewTicker(w.cfg.RetryDelay)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				_ = w.ProcessOnce(workerCtx)
			}
		}
	}()
	return nil
}

func (w *HandoffRetryWorker) Stop(ctx context.Context) error {
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
		w.mu.Lock()
		if w.done == done {
			w.done = nil
			w.cancel = nil
		}
		w.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *HandoffRetryWorker) ProcessOnce(ctx context.Context) error {
	if w == nil || w.outbox == nil {
		return errors.New("billing: incomplete handoff retry worker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	jobs, err := w.outbox.ClaimDue(ctx, 32)
	if err != nil {
		return fmt.Errorf("billing: claim handoff jobs: %w", err)
	}
	var allErr error
	for _, job := range jobs {
		if err := w.processJob(ctx, job); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	return allErr
}

func (w *HandoffRetryWorker) ProcessUntilIdle(ctx context.Context) error {
	if w == nil {
		return errors.New("billing: nil handoff retry worker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		pending, err := w.outbox.Pending(ctx)
		if err != nil {
			return err
		}
		if pending == 0 {
			return nil
		}
		if err := w.ProcessOnce(ctx); err != nil {
			return err
		}
		pending, err = w.outbox.Pending(ctx)
		if err != nil {
			return err
		}
		if pending == 0 {
			return nil
		}
		timer := time.NewTimer(w.cfg.RetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (w *HandoffRetryWorker) processJob(ctx context.Context, job HandoffRetryJob) error {
	aLegID := strings.TrimSpace(job.ALegID)
	if job.BarrierPending {
		return w.outbox.Defer(ctx, job, w.cfg.RetryDelay)
	}
	legs := sealableHandoffLegs(job.Legs)
	if len(legs) == 0 {
		job.NoEvidenceAttempts++
		if job.NoEvidenceAttempts >= w.cfg.NoEvidenceMaxAttempts {
			if job.UpstreamOpened {
				return w.outbox.Complete(ctx, aLegID)
			}
			if err := w.releaseUnused(ctx, job); err != nil && w.cfg.Log != nil {
				w.cfg.Log.DebugContext(ctx, "billing unused-hold release after handoff exhaustion failed", "a_leg_id", aLegID, "error", err)
			}
			return w.outbox.Complete(ctx, aLegID)
		}
		return w.outbox.Defer(ctx, job, w.retryAfter(job))
	}
	if w.append == nil {
		return w.outbox.Defer(ctx, job, w.retryAfter(job))
	}
	record, err := sealedHandoffRecord(job, legs)
	if err != nil {
		return w.outbox.Defer(ctx, job, w.retryAfter(job))
	}
	writeCtx, cancel := context.WithTimeout(ctx, w.cfg.WriteTimeout)
	err = w.append.AppendUsageRecord(writeCtx, record)
	cancel()
	if err != nil {
		job.EvidenceAttempts++
		if w.cfg.EvidenceMaxAttempts > 0 && job.EvidenceAttempts >= w.cfg.EvidenceMaxAttempts {
			if w.cfg.Log != nil {
				w.cfg.Log.DebugContext(ctx, "billing TUR handoff evidence retry budget exhausted; hold retained", "a_leg_id", aLegID)
			}
			return w.outbox.Complete(ctx, aLegID)
		}
		if w.cfg.Log != nil {
			w.cfg.Log.DebugContext(ctx, "billing TUR handoff retry continuing with evidence", "a_leg_id", aLegID, "error", err)
		}
		return w.outbox.Defer(ctx, job, w.retryAfter(job))
	}
	return w.outbox.Complete(ctx, aLegID)
}

func (w *HandoffRetryWorker) retryAfter(job HandoffRetryJob) time.Duration {
	n := job.NoEvidenceAttempts + job.EvidenceAttempts
	d := w.cfg.RetryDelay
	max := w.cfg.MaxRetryDelay
	for i := 1; i < n; i++ {
		next := d * 2
		if next > max || next < d {
			return max
		}
		d = next
	}
	if d > max {
		return max
	}
	return d
}

func (w *HandoffRetryWorker) releaseUnused(ctx context.Context, job HandoffRetryJob) error {
	if w == nil || w.release == nil {
		return nil
	}
	turKey, err := TURKey(job.AccountID, job.ALegID)
	if err != nil {
		return err
	}
	_, err = w.release.ReleaseAuthorization(ctx, ReleaseAuthorizationInput{
		AccountID: job.AccountID, AuthorizationID: job.AuthorizationID, TURKey: turKey,
		FullClose: true, Reason: ReleaseExecutionNotStarted,
		SourceKey: "handoff_exhausted:" + job.AuthorizationID,
	})
	return err
}

func sealedHandoffRecord(job HandoffRetryJob, legs []LegUsageRecord) (TurnUsageRecord, error) {
	sort.SliceStable(legs, func(i, j int) bool { return legs[i].Seq < legs[j].Seq })
	started, finished := legs[0].StartedAt, legs[0].FinishedAt
	for _, leg := range legs[1:] {
		if !leg.StartedAt.IsZero() && (started.IsZero() || leg.StartedAt.Before(started)) {
			started = leg.StartedAt
		}
		if leg.FinishedAt.After(finished) {
			finished = leg.FinishedAt
		}
	}
	outcome := job.Outcome
	if outcome == "" {
		outcome = TurnOutcomeUnknown
	}
	record := TurnUsageRecord{
		SchemaVersion:      CurrentRecordSchemaVersion,
		AccountID:          job.AccountID,
		TurnID:             job.ALegID,
		ALegID:             job.ALegID,
		AuthorizationID:    job.AuthorizationID,
		SessionID:          strings.TrimSpace(job.SessionID),
		StartedAt:          started,
		FinishedAt:         finished,
		Outcome:            outcome,
		CustomerPricingRef: job.CustomerPricing,
		ChargePolicyRef:    job.ChargePolicy,
		Legs:               legs,
	}
	return record.Seal()
}

func sealableHandoffLegs(legs []LegUsageRecord) []LegUsageRecord {
	out := make([]LegUsageRecord, 0, len(legs))
	for _, leg := range legs {
		if leg.StartedAt.IsZero() || leg.FinishedAt.IsZero() || leg.FinishedAt.Before(leg.StartedAt) {
			continue
		}
		out = append(out, leg)
	}
	return out
}
