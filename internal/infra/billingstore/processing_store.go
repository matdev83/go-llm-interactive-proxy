package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun/dialect"
)

const defaultProcessingLease = 5 * time.Minute

// defaultUnreconciledBackoff keeps unreconciled_cost rows out of the hot claim
// loop until repair/rate snapshots may be available again.
const defaultUnreconciledBackoff = time.Minute

// GetUsageRecord loads sealed evidence and never returns mutable processing
// fields as part of the immutable TUR/LUR value.
func (s *DurableStore) GetUsageRecord(ctx context.Context, turKey string) (billing.TurnUsageRecord, error) {
	if s == nil || s.db == nil {
		return billing.TurnUsageRecord{}, fmt.Errorf("billingstore: nil store")
	}
	var payload string
	if err := s.db.NewRaw(`SELECT payload_json FROM turn_usage_records WHERE tur_key = ?`, strings.TrimSpace(turKey)).Scan(ctx, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billing.TurnUsageRecord{}, ErrUsageRecordNotFound
		}
		return billing.TurnUsageRecord{}, err
	}
	var record billing.TurnUsageRecord
	if err := json.Unmarshal([]byte(payload), &record); err != nil {
		return billing.TurnUsageRecord{}, fmt.Errorf("billingstore: decode TUR: %w", err)
	}
	if err := billing.CheckReplay(record, record); err != nil {
		return billing.TurnUsageRecord{}, err
	}
	return record, nil
}

func (s *DurableStore) GetProcessing(ctx context.Context, turKey string) (billing.UsageRecordProcessing, error) {
	var row usageRecordProcessingRow
	if err := s.db.NewRaw(`SELECT tur_key, tur_fingerprint, status, lease_owner, lease_until, retry_count, safe_error_code, result_ref, updated_at FROM usage_record_processing WHERE tur_key = ?`, strings.TrimSpace(turKey)).Scan(ctx, &row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billing.UsageRecordProcessing{}, ErrProcessingNotFound
		}
		return billing.UsageRecordProcessing{}, err
	}
	return processingFromRow(row), nil
}

// ClaimPending atomically claims pending/retryable work and expired processing
// leases. The database is the queue; process restarts do not lose claims.
func (s *DurableStore) ClaimPending(ctx context.Context, limit int) ([]billing.TurnUsageRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("billingstore: nil store")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("billingstore: claim limit must be positive")
	}
	return withAccountTx(ctx, accountTxRetry{
		Attempts:  30,
		Delay:     3 * time.Millisecond,
		Exhausted: fmt.Errorf("%w: processing claim retry budget exhausted", billing.ErrAuthorizationUnavailable),
	}, func() ([]billing.TurnUsageRecord, error) {
		return s.claimPendingAttempt(ctx, limit)
	})
}

func (s *DurableStore) claimPendingAttempt(ctx context.Context, limit int) ([]billing.TurnUsageRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	leaseUntil := now.Add(defaultProcessingLease)
	var rows []usageRecordProcessingRow
	query := `SELECT tur_key, tur_fingerprint, status, lease_owner, lease_until, retry_count, safe_error_code, result_ref, updated_at FROM usage_record_processing WHERE (status IN ('pending','retryable') OR (status = 'unreconciled_cost' AND (lease_until IS NULL OR lease_until <= ?)) OR (status = 'processing' AND lease_until IS NOT NULL AND lease_until <= ?)) ORDER BY updated_at, tur_key LIMIT ?`
	if s.db.Dialect().Name() == dialect.PG {
		query += ` FOR UPDATE SKIP LOCKED`
	}
	if err := tx.NewRaw(query, now, now, limit).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("billingstore: select pending usage: %w", err)
	}
	if len(rows) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("billingstore: commit empty processing claim: %w", err)
		}
		return nil, nil
	}

	keys := make([]string, len(rows))
	for i, row := range rows {
		keys[i] = row.TURKey
	}
	inArgs, err := sqlInArgs(keys)
	if err != nil {
		return nil, err
	}
	updateArgs := make([]any, 0, 4+len(keys))
	updateArgs = append(updateArgs, s.storeID, leaseUntil, now)
	updateArgs = append(updateArgs, inArgs...)
	updateArgs = append(updateArgs, now)
	updateSQL := `UPDATE usage_record_processing SET status = 'processing', lease_owner = ?, lease_until = ?, updated_at = ? WHERE tur_key IN (` + sqlPlaceholders(len(keys)) + `) AND (status IN ('pending','retryable') OR (status = 'unreconciled_cost' AND (lease_until IS NULL OR lease_until <= ?)) OR (status = 'processing' AND lease_until IS NOT NULL AND lease_until <= ?)) RETURNING tur_key`
	var claimedRows []struct {
		TURKey string `bun:"tur_key"`
	}
	updateArgs = append(updateArgs, now) // second now for processing lease expiry predicate
	if err := tx.NewRaw(updateSQL, updateArgs...).Scan(ctx, &claimedRows); err != nil {
		return nil, fmt.Errorf("billingstore: claim usage batch: %w", err)
	}
	if len(claimedRows) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("billingstore: commit empty claimed batch: %w", err)
		}
		return nil, nil
	}
	claimedKeys := make([]string, len(claimedRows))
	for i, row := range claimedRows {
		claimedKeys[i] = row.TURKey
	}

	payloadArgs, err := sqlInArgs(claimedKeys)
	if err != nil {
		return nil, err
	}
	type payloadRow struct {
		TURKey  string `bun:"tur_key"`
		Payload string `bun:"payload_json"`
	}
	var payloads []payloadRow
	if err := tx.NewRaw(`SELECT tur_key, payload_json FROM turn_usage_records WHERE tur_key IN (`+sqlPlaceholders(len(claimedKeys))+`)`, payloadArgs...).Scan(ctx, &payloads); err != nil {
		return nil, fmt.Errorf("billingstore: load claimed TUR payloads: %w", err)
	}
	byKey := make(map[string]string, len(payloads))
	for _, row := range payloads {
		byKey[row.TURKey] = row.Payload
	}
	// Decode inside the claim transaction so corrupt payloads roll the lease back
	// to pending/retryable eligibility instead of burning a processing lease.
	out := make([]billing.TurnUsageRecord, 0, len(claimedKeys))
	for _, key := range claimedKeys {
		payload, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("billingstore: load claimed TUR %s: %w", key, ErrUsageRecordNotFound)
		}
		var record billing.TurnUsageRecord
		if err := json.Unmarshal([]byte(payload), &record); err != nil {
			return nil, fmt.Errorf("billingstore: decode claimed TUR %s: %w", key, err)
		}
		if err := billing.CheckReplay(record, record); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("billingstore: commit processing claim: %w", err)
	}
	return out, nil
}

func (s *DurableStore) MarkProcessingRetryable(ctx context.Context, turKey, fingerprint, safeErrorCode string) error {
	return s.updateProcessing(ctx, turKey, fingerprint, billing.ProcessingRetryable, safeErrorCode, "")
}

func (s *DurableStore) MarkProcessingTerminal(ctx context.Context, turKey, fingerprint, safeErrorCode string) error {
	return s.updateProcessing(ctx, turKey, fingerprint, billing.ProcessingTerminalError, safeErrorCode, "")
}

func (s *DurableStore) MarkProcessingUnreconciledCost(ctx context.Context, turKey, fingerprint, safeErrorCode string) error {
	if strings.TrimSpace(turKey) == "" || strings.TrimSpace(fingerprint) == "" {
		return billing.ErrProcessingInvalid
	}
	now := time.Now().UTC()
	backoffUntil := now.Add(defaultUnreconciledBackoff)
	result, err := s.db.NewRaw(`UPDATE usage_record_processing SET status = ?, lease_owner = '', lease_until = ?, retry_count = retry_count, safe_error_code = ?, result_ref = '', updated_at = ? WHERE tur_key = ? AND tur_fingerprint = ? AND status = 'processing' AND lease_owner = ?`, string(billing.ProcessingUnreconciledCost), backoffUntil, strings.TrimSpace(safeErrorCode), now, strings.TrimSpace(turKey), strings.TrimSpace(fingerprint), s.storeID).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: update processing %s: %w", turKey, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 1 {
		return nil
	}
	return s.processingMarkConflict(ctx, turKey, fingerprint)
}

func (s *DurableStore) MarkProcessingProcessed(ctx context.Context, turKey, fingerprint, resultRef string) error {
	return s.updateProcessing(ctx, turKey, fingerprint, billing.ProcessingProcessed, "", resultRef)
}

func (s *DurableStore) MarkProcessingInvariantFailure(ctx context.Context, record billing.TurnUsageRecord, safeErrorCode string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	turKey := strings.TrimSpace(record.Key)
	fingerprint := strings.TrimSpace(record.Fingerprint)
	accountID := strings.TrimSpace(record.AccountID)
	if turKey == "" || fingerprint == "" || accountID == "" {
		return billing.ErrProcessingInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAccount(ctx, tx, s.db.Dialect().Name(), accountID); err != nil {
		return err
	}
	if err := lockProcessingForSettlement(ctx, tx, s.db.Dialect().Name(), turKey); err != nil {
		return err
	}
	var existing usageRecordProcessingRow
	if err := tx.NewRaw(`SELECT tur_key, tur_fingerprint, status, lease_owner, lease_until, retry_count, safe_error_code, result_ref, updated_at FROM usage_record_processing WHERE tur_key = ?`, turKey).Scan(ctx, &existing); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrProcessingNotFound
		}
		return err
	}
	if existing.TURFingerprint != fingerprint {
		return billing.ErrProcessingConflict
	}
	if existing.Status != string(billing.ProcessingProcessing) || existing.LeaseOwner != s.storeID {
		if existing.Status == string(billing.ProcessingProcessing) && existing.LeaseOwner != "" && existing.LeaseOwner != s.storeID {
			return fmt.Errorf("%w: owned by %q", billing.ErrProcessingLeaseConflict, existing.LeaseOwner)
		}
		return fmt.Errorf("%w: expected processing claim, current status %s", billing.ErrProcessingInvalid, existing.Status)
	}
	if err := setReconcileRequiredTx(ctx, tx, accountID); err != nil {
		return err
	}
	now := time.Now().UTC()
	result, err := tx.NewRaw(`UPDATE usage_record_processing SET status = ?, lease_owner = '', lease_until = NULL, retry_count = retry_count, safe_error_code = ?, result_ref = '', updated_at = ? WHERE tur_key = ? AND tur_fingerprint = ? AND status = 'processing' AND lease_owner = ?`, string(billing.ProcessingTerminalError), strings.TrimSpace(safeErrorCode), now, turKey, fingerprint, s.storeID).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: invariant processing %s: %w", turKey, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("%w: expected processing claim, current status %s", billing.ErrProcessingInvalid, existing.Status)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: commit invariant failure: %w", err)
	}
	return nil
}

func (s *DurableStore) updateProcessing(ctx context.Context, turKey, fingerprint string, status billing.ProcessingStatus, safeErrorCode, resultRef string) error {
	if !status.Valid() || strings.TrimSpace(turKey) == "" || strings.TrimSpace(fingerprint) == "" {
		return billing.ErrProcessingInvalid
	}
	result, err := s.db.NewRaw(`UPDATE usage_record_processing SET status = ?, lease_owner = '', lease_until = NULL, retry_count = CASE WHEN ? = 'retryable' THEN retry_count + 1 ELSE retry_count END, safe_error_code = ?, result_ref = ?, updated_at = ? WHERE tur_key = ? AND tur_fingerprint = ? AND status = 'processing' AND lease_owner = ?`, string(status), string(status), strings.TrimSpace(safeErrorCode), strings.TrimSpace(resultRef), time.Now().UTC(), strings.TrimSpace(turKey), strings.TrimSpace(fingerprint), s.storeID).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: update processing %s: %w", turKey, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 1 {
		return nil
	}
	return s.processingMarkConflict(ctx, turKey, fingerprint)
}

func (s *DurableStore) processingMarkConflict(ctx context.Context, turKey, fingerprint string) error {
	var existing usageRecordProcessingRow
	if err := s.db.NewRaw(`SELECT tur_key, tur_fingerprint, status, lease_owner, lease_until, retry_count, safe_error_code, result_ref, updated_at FROM usage_record_processing WHERE tur_key = ?`, turKey).Scan(ctx, &existing); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrProcessingNotFound
		}
		return err
	}
	if existing.TURFingerprint != fingerprint {
		return billing.ErrProcessingConflict
	}
	if existing.Status == string(billing.ProcessingProcessing) && existing.LeaseOwner != "" && existing.LeaseOwner != s.storeID {
		return fmt.Errorf("%w: owned by %q", billing.ErrProcessingLeaseConflict, existing.LeaseOwner)
	}
	return fmt.Errorf("%w: expected processing claim, current status %s", billing.ErrProcessingInvalid, existing.Status)
}

func processingFromRow(row usageRecordProcessingRow) billing.UsageRecordProcessing {
	return billing.UsageRecordProcessing{TURKey: row.TURKey, TURFingerprint: row.TURFingerprint, Status: billing.ProcessingStatus(row.Status), LeaseOwner: row.LeaseOwner, LeaseUntil: row.LeaseUntil, RetryCount: row.RetryCount, SafeErrorCode: row.SafeErrorCode, ResultRef: row.ResultRef, UpdatedAt: row.UpdatedAt}
}

var _ billing.PostTurnStore = (*DurableStore)(nil)
