package billingstore

// This file is retained only as an isolated brownfield migration/test adapter.
// No runtime composition or authoritative terminal path calls these legacy
// usage_append_outbox writer/reader methods; new terminal delivery is owned by
// the process-local billingspool package.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

const (
	usageAppendOutboxBaseRetryDelay = time.Second
	usageAppendOutboxMaxRetryDelay  = time.Hour
)

func (s *DurableStore) EnqueueCallUsageAppend(ctx context.Context, record billing.CallUsageRecord, cause string) error {
	sealed, err := record.Seal()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		return fmt.Errorf("billingstore: encode failed call usage append: %w", err)
	}
	return s.enqueueUsageAppend(ctx, sealed.Key, billing.UsageAppendCall, sealed.CallID, payload, cause)
}

func (s *DurableStore) EnqueueCallLegUsageAppend(ctx context.Context, record billing.CallLegUsageRecord, cause string) error {
	sealed, err := record.Seal()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		return fmt.Errorf("billingstore: encode failed call-leg usage append: %w", err)
	}
	return s.enqueueUsageAppend(ctx, sealed.Key, billing.UsageAppendLeg, sealed.CallID, payload, cause)
}

func (s *DurableStore) enqueueUsageAppend(ctx context.Context, key string, kind billing.UsageAppendKind, callID billing.BillingCallID, payload []byte, cause string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	if err := callID.Validate(); err != nil {
		return err
	}
	cause = strings.TrimSpace(cause)
	now := time.Now().UTC()
	_, err := s.db.NewRaw(`INSERT INTO usage_append_outbox( append_key, kind, call_id, payload_json, status, attempt_count, next_attempt_at, last_error, created_at, updated_at ) VALUES (?, ?, ?, ?, 'pending', 0, ?, ?, ?, ?) ON CONFLICT(append_key) DO NOTHING`, key, string(kind), callID.String(), string(payload), now, cause, now, now).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: enqueue usage append: %w", err)
	}
	return nil
}

func (s *DurableStore) ListPendingUsageAppendWork(ctx context.Context, limit int) ([]billing.UsageAppendWork, error) {
	return s.listUsageAppendWork(ctx, limit, true)
}

// listAllPendingUsageAppendWork is used only by the destructive cutover. It
// includes deferred rows regardless of next_attempt_at; migration must not
// mistake a future retry schedule for proof of delivery.
func (s *DurableStore) listAllPendingUsageAppendWork(ctx context.Context, limit int) ([]billing.UsageAppendWork, error) {
	return s.listUsageAppendWork(ctx, limit, false)
}

func (s *DurableStore) listUsageAppendWork(ctx context.Context, limit int, dueOnly bool) ([]billing.UsageAppendWork, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("billingstore: nil store")
	}
	if limit <= 0 {
		limit = 32
	}
	if err := s.pruneProcessedUsageAppendWork(ctx, time.Now().UTC().Add(-24*time.Hour)); err != nil {
		return nil, err
	}
	type row struct {
		Key     string `bun:"append_key"`
		Kind    string `bun:"kind"`
		CallID  string `bun:"call_id"`
		Payload string `bun:"payload_json"`
	}
	query := `SELECT append_key, kind, call_id, payload_json FROM usage_append_outbox WHERE status = 'pending'`
	args := []any{}
	if dueOnly {
		query += ` AND next_attempt_at <= ?`
		args = append(args, time.Now().UTC())
	}
	query += ` ORDER BY next_attempt_at, updated_at, append_key LIMIT ?`
	args = append(args, limit)
	var rows []row
	if err := s.db.NewRaw(query, args...).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("billingstore: list pending usage appends: %w", err)
	}
	out := make([]billing.UsageAppendWork, 0, len(rows))
	for _, item := range rows {
		callID, err := billing.ParseBillingCallID(item.CallID)
		if err != nil {
			return nil, fmt.Errorf("billingstore: parse usage append call ID: %w", err)
		}
		work := billing.UsageAppendWork{Key: item.Key, Kind: billing.UsageAppendKind(item.Kind)}
		switch work.Kind {
		case billing.UsageAppendCall:
			var record billing.CallUsageRecord
			if err := json.Unmarshal([]byte(item.Payload), &record); err != nil {
				return nil, fmt.Errorf("billingstore: decode failed call usage append: %w", err)
			}
			sealed, err := record.Seal()
			if err != nil {
				return nil, fmt.Errorf("billingstore: seal failed call usage append: %w", err)
			}
			if sealed.Key != item.Key || sealed.CallID != callID {
				return nil, fmt.Errorf("billingstore: failed call usage append identity mismatch: %s", item.Key)
			}
			work.Call = &sealed
		case billing.UsageAppendLeg:
			var record billing.CallLegUsageRecord
			if err := json.Unmarshal([]byte(item.Payload), &record); err != nil {
				return nil, fmt.Errorf("billingstore: decode failed call-leg usage append: %w", err)
			}
			sealed, err := record.Seal()
			if err != nil {
				return nil, fmt.Errorf("billingstore: seal failed call-leg usage append: %w", err)
			}
			if sealed.Key != item.Key || sealed.CallID != callID {
				return nil, fmt.Errorf("billingstore: failed call-leg usage append identity mismatch: %s", item.Key)
			}
			work.Leg = &sealed
		default:
			return nil, fmt.Errorf("billingstore: unsupported usage append kind %q", item.Kind)
		}
		out = append(out, work)
	}
	return out, nil
}

func (s *DurableStore) MarkUsageAppendProcessed(ctx context.Context, key string) error {
	return s.updateUsageAppendStatus(ctx, key, "processed", "", "pending")
}

func (s *DurableStore) FailUsageAppend(ctx context.Context, key, reason string) error {
	return s.updateUsageAppendStatus(ctx, key, "failed", strings.TrimSpace(reason), "pending")
}

func (s *DurableStore) updateUsageAppendStatus(ctx context.Context, key, status, reason, from string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("billingstore: usage append key is required")
	}
	result, err := s.db.NewRaw(`UPDATE usage_append_outbox SET status = ?, last_error = ?, updated_at = ? WHERE append_key = ? AND status = ?`, status, reason, time.Now().UTC(), key, from).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: update usage append status: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("billingstore: usage append status rows affected: %w", err)
	} else if count == 0 {
		var existing string
		if scanErr := s.db.NewRaw(`SELECT status FROM usage_append_outbox WHERE append_key = ?`, key).Scan(ctx, &existing); errors.Is(scanErr, sql.ErrNoRows) {
			return fmt.Errorf("billingstore: usage append work not found: %s", key)
		} else if scanErr != nil {
			return fmt.Errorf("billingstore: inspect usage append status: %w", scanErr)
		}
	}
	return nil
}

func (s *DurableStore) DeferUsageAppend(ctx context.Context, key, reason string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("billingstore: usage append key is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: begin usage append defer: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	var attempts int
	if err := tx.NewRaw(`UPDATE usage_append_outbox SET attempt_count = attempt_count + 1, last_error = ?, updated_at = ? WHERE append_key = ? AND status = 'pending' RETURNING attempt_count`, strings.TrimSpace(reason), now, key).Scan(ctx, &attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("billingstore: usage append work not found or terminal: %s", key)
		}
		return fmt.Errorf("billingstore: defer usage append: %w", err)
	}
	delay := retryBackoffDelay(usageAppendOutboxBaseRetryDelay, usageAppendOutboxMaxRetryDelay, attempts)
	if _, err := tx.NewRaw(`UPDATE usage_append_outbox SET next_attempt_at = ? WHERE append_key = ? AND status = 'pending' AND attempt_count = ?`, now.Add(delay), key, attempts).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: schedule usage append retry: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: commit usage append defer: %w", err)
	}
	return nil
}

func (s *DurableStore) pruneProcessedUsageAppendWork(ctx context.Context, before time.Time) error {
	_, err := s.db.NewRaw(`
DELETE FROM usage_append_outbox
WHERE append_key IN (
	SELECT append_key FROM usage_append_outbox
	WHERE status = 'processed' AND updated_at < ?
	ORDER BY updated_at
	LIMIT 256
)`, before).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: prune processed usage appends: %w", err)
	}
	return nil
}
