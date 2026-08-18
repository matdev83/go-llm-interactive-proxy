package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

const (
	providerCostWorkBaseRetryDelay = time.Second
	providerCostWorkMaxRetryDelay  = time.Hour
)

var _ billing.ProviderCostWorkFailureStore = (*DurableStore)(nil)

// ProviderCostWorkState is the durable retry state for one provider-cost leg.
// It intentionally exposes scheduling metadata without making deferred work
// appear due to ListPendingProviderCostWork.
type ProviderCostWorkState struct {
	Status        string
	AttemptCount  int
	NextAttemptAt time.Time
	LastError     string
}

// GetProviderCostWorkState reads provider-cost work independently of whether
// its next retry is due. This is used by operators and tests to distinguish
// durable pending/unreconciled work from an empty due queue.
func (s *DurableStore) GetProviderCostWorkState(ctx context.Context, legKey string) (ProviderCostWorkState, error) {
	if s == nil || s.db == nil {
		return ProviderCostWorkState{}, fmt.Errorf("billingstore: nil store")
	}
	legKey = strings.TrimSpace(legKey)
	if legKey == "" {
		return ProviderCostWorkState{}, fmt.Errorf("billingstore: provider-cost work key is required")
	}
	var state ProviderCostWorkState
	if err := s.db.NewRaw(`
SELECT status, attempt_count, next_attempt_at, last_error
FROM provider_cost_work
WHERE usage_leg_key = ?`, legKey).Scan(ctx, &state.Status, &state.AttemptCount, &state.NextAttemptAt, &state.LastError); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProviderCostWorkState{}, ErrUsageRecordNotFound
		}
		return ProviderCostWorkState{}, fmt.Errorf("billingstore: get provider-cost work state: %w", err)
	}
	return state, nil
}

func (s *DurableStore) pruneProcessedProviderCostWork(ctx context.Context, before time.Time) error {
	_, err := s.db.NewRaw(`
DELETE FROM provider_cost_work
WHERE usage_leg_key IN (
	SELECT usage_leg_key FROM provider_cost_work
	WHERE status = 'processed' AND updated_at < ?
	ORDER BY updated_at
	LIMIT 256
)`, before).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: prune processed provider-cost work: %w", err)
	}
	return nil
}

func (s *DurableStore) DeferProviderCostWork(ctx context.Context, work billing.ProviderCostWork, reason string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	if err := work.CallID.Validate(); err != nil {
		return err
	}
	leg, err := work.Leg.Seal()
	if err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "provider_cost_failed"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: begin provider-cost defer: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	var attempts int
	if err := tx.NewRaw(`
UPDATE provider_cost_work
SET attempt_count = attempt_count + 1, last_error = ?, updated_at = ?
WHERE usage_leg_key = ? AND status = 'pending'
RETURNING attempt_count`, reason, now, leg.Key).Scan(ctx, &attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("billingstore: provider-cost work not found: %s", leg.Key)
		}
		return fmt.Errorf("billingstore: defer provider-cost work: %w", err)
	}
	delay := retryBackoffDelay(providerCostWorkBaseRetryDelay, providerCostWorkMaxRetryDelay, attempts)
	if _, err := tx.NewRaw(`UPDATE provider_cost_work SET next_attempt_at = ? WHERE usage_leg_key = ? AND status = 'pending' AND attempt_count = ?`, now.Add(delay), leg.Key, attempts).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: schedule provider-cost retry: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: commit provider-cost defer: %w", err)
	}
	return nil
}

func retryBackoffDelay(base, max time.Duration, attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := base
	if shift := attempts - 1; shift > 0 {
		if shift > 16 {
			shift = 16
		}
		delay = time.Duration(float64(delay) * math.Pow(2, float64(shift)))
		if delay > max {
			delay = max
		}
	}
	return delay
}
