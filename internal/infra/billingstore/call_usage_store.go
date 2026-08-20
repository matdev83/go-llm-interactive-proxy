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
	"github.com/uptrace/bun"
)

const (
	usageCallClaimPending           = "pending"
	usageCallClaimClaimed           = "claimed"
	usageCallClaimProcessed         = "processed"
	usageCallClaimReconcileRequired = "reconcile_required"
	completeCallClaimLease          = 2 * time.Minute
	completeCallIncompleteYield     = time.Second
	completeCallClaimBaseRetry      = time.Second
	completeCallClaimMaxRetry       = time.Hour
	completeCallClaimMaxAttempts    = 20
)

type rawQueryDB interface {
	NewRaw(query string, args ...any) *bun.RawQuery
}

// AppendCallUsage is the DurableStore's current-record persistence API. It is
// retained for historical outbox drain/storage and is not a runtime transport
// interface; runtime terminal handoff uses billing.TerminalUsageSink.
func (s *DurableStore) AppendCallUsage(ctx context.Context, record billing.CallUsageRecord) error {
	return withAccountTxErr(ctx, accountTxRetry{Attempts: 20, Delay: 5 * time.Millisecond}, func() error {
		return s.appendCallUsageAttempt(ctx, record)
	})
}

func (s *DurableStore) appendCallUsageAttempt(ctx context.Context, record billing.CallUsageRecord) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	sealed, err := record.Seal()
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: begin call usage append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var existingPayload string
	err = tx.NewRaw(`SELECT payload_json FROM usage_call_records WHERE usage_call_key = ?`, sealed.Key).Scan(ctx, &existingPayload)
	if err == nil {
		var existing billing.CallUsageRecord
		if unmarshalErr := json.Unmarshal([]byte(existingPayload), &existing); unmarshalErr != nil {
			return fmt.Errorf("billingstore: decode existing call usage: %w", unmarshalErr)
		}
		if replayErr := billing.CheckCallUsageReplay(existing, sealed); replayErr != nil {
			return replayErr
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("billingstore: commit call usage replay: %w", err)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("billingstore: lookup call usage: %w", err)
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		return fmt.Errorf("billingstore: encode call usage: %w", err)
	}
	expectedJSON, err := json.Marshal(sealed.ExpectedBLegIDs)
	if err != nil {
		return fmt.Errorf("billingstore: encode expected B-leg IDs: %w", err)
	}
	sealedAt := time.Now().UTC()
	_, err = tx.NewRaw(`INSERT INTO usage_call_records( usage_call_key, fingerprint, call_id, account_id, a_leg_id, session_id, started_at, finished_at, outcome, expected_b_leg_ids, payload_json, sealed_at, claim_status, claim_attempt_count, next_claim_at, last_claim_error ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, sealed.Key, sealed.Fingerprint, sealed.CallID.String(), sealed.AccountID, sealed.ALegID, sealed.SessionID,
		sealed.StartedAt, sealed.FinishedAt, string(sealed.Outcome), string(expectedJSON), string(payload), sealedAt,
		usageCallClaimPending, 0, sealedAt, "").Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: insert call usage: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: commit call usage: %w", err)
	}
	return nil
}

func (s *DurableStore) ListCallUsage(ctx context.Context, accountID string) ([]billing.CallUsageRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("billingstore: nil store")
	}
	accountID = strings.TrimSpace(accountID)
	var payloads []string
	var err error
	if accountID == "" {
		err = s.db.NewRaw(`SELECT payload_json FROM usage_call_records ORDER BY sealed_at`).Scan(ctx, &payloads)
	} else {
		err = s.db.NewRaw(`SELECT payload_json FROM usage_call_records WHERE account_id = ? ORDER BY sealed_at`, accountID).Scan(ctx, &payloads)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("billingstore: list call usage: %w", err)
	}
	out := make([]billing.CallUsageRecord, 0, len(payloads))
	for _, payload := range payloads {
		var record billing.CallUsageRecord
		if err := json.Unmarshal([]byte(payload), &record); err != nil {
			return nil, fmt.Errorf("billingstore: decode call usage: %w", err)
		}
		if err := billing.CheckCallUsageReplay(record, record); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

func (s *DurableStore) ListCallLegUsage(ctx context.Context, callID billing.BillingCallID) ([]billing.CallLegUsageRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("billingstore: nil store")
	}
	if err := callID.Validate(); err != nil {
		return nil, err
	}
	return s.loadCallLegUsageByCall(ctx, s.db, callID)
}

func (s *DurableStore) GetCallUsage(ctx context.Context, callID billing.BillingCallID) (billing.CallUsageRecord, error) {
	if s == nil || s.db == nil {
		return billing.CallUsageRecord{}, fmt.Errorf("billingstore: nil store")
	}
	if err := callID.Validate(); err != nil {
		return billing.CallUsageRecord{}, err
	}
	return s.loadCallUsage(ctx, s.db, callID)
}

func (s *DurableStore) ClaimCompleteCalls(ctx context.Context, limit int) ([]billing.CompleteCall, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("billingstore: nil store")
	}
	if limit <= 0 {
		limit = 32
	}
	maxCandidates := max(limit*8, 256)
	out := make([]billing.CompleteCall, 0, limit)
	var afterSealed time.Time
	var afterCallID string
	scanned := 0
	now := time.Now().UTC()
	staleBefore := now.Add(-completeCallClaimLease)
	for scanned < maxCandidates && len(out) < limit {
		pageSize := limit
		if remaining := maxCandidates - scanned; remaining < pageSize {
			pageSize = remaining
		}
		if pageSize <= 0 {
			break
		}
		type pendingCall struct {
			CallID   string
			SealedAt time.Time
		}
		var page []pendingCall
		var err error
		if afterCallID == "" {
			err = s.db.NewRaw(
				`SELECT call_id, sealed_at FROM usage_call_records
 WHERE (
   (claim_status = ? AND next_claim_at <= ?)
   OR (claim_status = ? AND claimed_at IS NOT NULL AND claimed_at <= ?)
 )
 ORDER BY sealed_at, call_id LIMIT ?`,
				usageCallClaimPending, now, usageCallClaimClaimed, staleBefore, pageSize,
			).Scan(ctx, &page)
		} else {
			err = s.db.NewRaw(
				`SELECT call_id, sealed_at FROM usage_call_records
 WHERE (
   (claim_status = ? AND next_claim_at <= ?)
   OR (claim_status = ? AND claimed_at IS NOT NULL AND claimed_at <= ?)
 )
   AND (sealed_at > ? OR (sealed_at = ? AND call_id > ?))
 ORDER BY sealed_at, call_id LIMIT ?`,
				usageCallClaimPending, now, usageCallClaimClaimed, staleBefore,
				afterSealed, afterSealed, afterCallID, pageSize,
			).Scan(ctx, &page)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		ids := make([]string, 0, len(page))
		for _, row := range page {
			ids = append(ids, row.CallID)
			afterSealed = row.SealedAt
			afterCallID = row.CallID
		}
		scanned += len(page)
		batch, err := s.claimCompleteCallsFromIDs(ctx, ids, limit-len(out), claimCompleteOpts{WorkerBatch: true, Now: now})
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(page) < pageSize {
			break
		}
	}
	return out, nil
}

type claimCompleteOpts struct {
	WorkerBatch bool
	Now         time.Time
}

func (s *DurableStore) claimCompleteCallsFromIDs(ctx context.Context, ids []string, limit int, opts claimCompleteOpts) ([]billing.CompleteCall, error) {
	if limit <= 0 {
		return nil, nil
	}
	out := make([]billing.CompleteCall, 0, limit)
	for _, raw := range ids {
		if len(out) >= limit {
			break
		}
		callID, err := billing.ParseBillingCallID(raw)
		if err != nil {
			return nil, err
		}
		complete, err := s.claimCompleteCallWithOpts(ctx, callID, opts)
		if err != nil {
			if errors.Is(err, billing.ErrCallIncomplete) {
				if opts.WorkerBatch {
					if deferErr := s.deferIncompleteCall(ctx, callID, opts.Now); deferErr != nil {
						return nil, errors.Join(err, deferErr)
					}
				}
				continue
			}
			if errors.Is(err, billing.ErrCallClaimConflict) {
				continue
			}
			return nil, err
		}
		out = append(out, complete)
	}
	return out, nil
}

func (s *DurableStore) deferIncompleteCall(ctx context.Context, callID billing.BillingCallID, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	next := now.Add(completeCallIncompleteYield)
	_, err := s.db.NewRaw(
		`UPDATE usage_call_records SET next_claim_at = ? WHERE call_id = ? AND claim_status = ? AND next_claim_at <= ?`,
		next, callID.String(), usageCallClaimPending, now,
	).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: defer incomplete call: %w", err)
	}
	return nil
}

func (s *DurableStore) GetCallExposure(ctx context.Context, callID billing.BillingCallID) (billing.CallExposure, error) {
	if s == nil || s.db == nil {
		return billing.CallExposure{}, fmt.Errorf("billingstore: nil store")
	}
	if err := callID.Validate(); err != nil {
		return billing.CallExposure{}, err
	}
	var row exposureRow
	if err := s.db.NewRaw(`SELECT exposure_key, account_id, call_id, max_exposure_nano, currency, pricing_ref, charge_policy_ref, fingerprint, balance_nano, credit_floor_nano, open_exposure_nano, settled_headroom_nano, safety_margin_before_nano, safety_margin_after_nano, status, created_at, closed_at FROM call_exposures WHERE call_id = ?`, callID.String()).Scan(ctx, &row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billing.CallExposure{}, billing.ErrExposureNotFound
		}
		return billing.CallExposure{}, err
	}
	return exposureFromRow(row)
}

func (s *DurableStore) RetryCompleteCall(ctx context.Context, callID billing.BillingCallID, code string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	if err := callID.Validate(); err != nil {
		return err
	}
	code = strings.TrimSpace(code)
	now := time.Now().UTC()
	if code == "settlement_reconcile_required" {
		_, err := s.db.NewRaw(
			`UPDATE usage_call_records SET claim_status = ?, claimed_at = NULL, last_claim_error = ?, next_claim_at = ? WHERE call_id = ? AND claim_status = ?`,
			usageCallClaimReconcileRequired, code, now, callID.String(), usageCallClaimClaimed,
		).Exec(ctx)
		if err != nil {
			return fmt.Errorf("billingstore: mark complete-call reconcile required: %w", err)
		}
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: begin complete-call retry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var attempts int
	if err := tx.NewRaw(`UPDATE usage_call_records SET claim_attempt_count = claim_attempt_count + 1, last_claim_error = ?, claimed_at = NULL WHERE call_id = ? AND claim_status = ? RETURNING claim_attempt_count`, code, callID.String(), usageCallClaimClaimed).Scan(ctx, &attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("billingstore: retry complete-call claim: %w", err)
	}
	status := usageCallClaimPending
	nextAt := now.Add(retryBackoffDelay(completeCallClaimBaseRetry, completeCallClaimMaxRetry, attempts))
	if attempts >= completeCallClaimMaxAttempts {
		status = usageCallClaimReconcileRequired
		nextAt = now
	}
	if _, err := tx.NewRaw(`UPDATE usage_call_records SET claim_status = ?, next_claim_at = ? WHERE call_id = ? AND claim_attempt_count = ?`, status, nextAt, callID.String(), attempts).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: schedule complete-call retry: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: commit complete-call retry: %w", err)
	}
	return nil
}

func (s *DurableStore) ClaimCompleteCall(ctx context.Context, callID billing.BillingCallID) (billing.CompleteCall, error) {
	return s.claimCompleteCallWithOpts(ctx, callID, claimCompleteOpts{Now: time.Now().UTC()})
}

func (s *DurableStore) claimCompleteCallWithOpts(ctx context.Context, callID billing.BillingCallID, opts claimCompleteOpts) (billing.CompleteCall, error) {
	var zero billing.CompleteCall
	if s == nil || s.db == nil {
		return zero, fmt.Errorf("billingstore: nil store")
	}
	if err := callID.Validate(); err != nil {
		return zero, err
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	return withAccountTx(ctx, accountTxRetry{Attempts: 20, Delay: 5 * time.Millisecond}, func() (billing.CompleteCall, error) {
		return s.claimCompleteCallAttempt(ctx, callID, opts)
	})
}

func (s *DurableStore) claimCompleteCallAttempt(ctx context.Context, callID billing.BillingCallID, opts claimCompleteOpts) (billing.CompleteCall, error) {
	var zero billing.CompleteCall
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, fmt.Errorf("billingstore: begin complete-call claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	closure, err := s.loadCallUsage(ctx, tx, callID)
	if errors.Is(err, ErrUsageRecordNotFound) {
		return zero, billing.ErrCallIncomplete
	}
	if err != nil {
		return zero, err
	}
	legs, err := s.loadCallLegUsageByCall(ctx, tx, callID)
	if err != nil {
		return zero, err
	}
	complete, err := billing.JoinCompleteCall(closure, legs)
	if err != nil {
		return zero, err
	}
	now := opts.Now
	staleBefore := now.Add(-completeCallClaimLease)
	var claimResult sql.Result
	if opts.WorkerBatch {
		claimResult, err = tx.NewRaw(
			`UPDATE usage_call_records SET claim_status = ?, claimed_at = ? WHERE call_id = ? AND ( (claim_status = ? AND next_claim_at <= ?) OR (claim_status = ? AND claimed_at IS NOT NULL AND claimed_at <= ?) )`,
			usageCallClaimClaimed, now, callID.String(),
			usageCallClaimPending, now,
			usageCallClaimClaimed, staleBefore,
		).Exec(ctx)
	} else {
		claimResult, err = tx.NewRaw(
			`UPDATE usage_call_records SET claim_status = ?, claimed_at = ? WHERE call_id = ? AND ( claim_status = ? OR claim_status = ? OR (claim_status = ? AND claimed_at IS NOT NULL AND claimed_at <= ?) )`,
			usageCallClaimClaimed, now, callID.String(),
			usageCallClaimPending,
			usageCallClaimReconcileRequired,
			usageCallClaimClaimed, staleBefore,
		).Exec(ctx)
	}
	if err != nil {
		return zero, fmt.Errorf("billingstore: mark complete call claimed: %w", err)
	}
	count, err := claimResult.RowsAffected()
	if err != nil {
		return zero, fmt.Errorf("billingstore: complete-call claim rows affected: %w", err)
	}
	if count != 1 {
		var status string
		if err := tx.NewRaw(`SELECT claim_status FROM usage_call_records WHERE call_id = ?`, callID.String()).Scan(ctx, &status); err != nil {
			return zero, fmt.Errorf("billingstore: inspect complete-call claim: %w", err)
		}
		if status == usageCallClaimProcessed {
			if err := tx.Commit(); err != nil {
				return zero, fmt.Errorf("billingstore: commit processed-call replay: %w", err)
			}
			return complete, nil
		}
		return zero, billing.ErrCallClaimConflict
	}
	if err := tx.Commit(); err != nil {
		return zero, fmt.Errorf("billingstore: commit complete-call claim: %w", err)
	}
	return complete, nil
}

func (s *DurableStore) loadCallUsage(ctx context.Context, q rawQueryDB, callID billing.BillingCallID) (billing.CallUsageRecord, error) {
	var payload string
	if err := q.NewRaw(`SELECT payload_json FROM usage_call_records WHERE call_id = ?`, callID.String()).Scan(ctx, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billing.CallUsageRecord{}, ErrUsageRecordNotFound
		}
		return billing.CallUsageRecord{}, err
	}
	var record billing.CallUsageRecord
	if err := json.Unmarshal([]byte(payload), &record); err != nil {
		return billing.CallUsageRecord{}, fmt.Errorf("billingstore: decode call usage: %w", err)
	}
	if err := billing.CheckCallUsageReplay(record, record); err != nil {
		return billing.CallUsageRecord{}, err
	}
	return record, nil
}

func (s *DurableStore) loadCallLegUsageByCall(ctx context.Context, q rawQueryDB, callID billing.BillingCallID) ([]billing.CallLegUsageRecord, error) {
	var payloads []string
	err := q.NewRaw(`SELECT payload_json FROM usage_leg_records WHERE call_id = ?`, callID.String()).Scan(ctx, &payloads)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("billingstore: list call-leg usage: %w", err)
	}
	out := make([]billing.CallLegUsageRecord, 0, len(payloads))
	for _, payload := range payloads {
		var record billing.CallLegUsageRecord
		if unmarshalErr := json.Unmarshal([]byte(payload), &record); unmarshalErr != nil {
			return nil, fmt.Errorf("billingstore: decode call-leg usage: %w", unmarshalErr)
		}
		out = append(out, record)
	}
	return out, nil
}
