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
)

// ErrLegAttemptSequenceConflict identifies duplicate positive sequences within one BillingCallID; legacy NULL values remain outside it.
var ErrLegAttemptSequenceConflict = errors.New("billingstore: call-leg attempt sequence conflict")

func (s *DurableStore) AppendCallLegUsage(ctx context.Context, record billing.CallLegUsageRecord) error {
	return withAccountTxErr(ctx, accountTxRetry{
		Attempts: 20,
		Delay:    5 * time.Millisecond,
		Classify: func(err error) error {
			if errors.Is(err, ErrLegAttemptSequenceConflict) {
				return err
			}
			return nil
		},
	}, func() error {
		return s.appendCallLegUsageAttempt(ctx, record)
	})
}

func (s *DurableStore) appendCallLegUsageAttempt(ctx context.Context, record billing.CallLegUsageRecord) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	sealed, err := record.Seal()
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: begin call-leg usage append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var existingPayload string
	err = tx.NewRaw(`SELECT payload_json FROM usage_leg_records WHERE usage_leg_key = ?`, sealed.Key).Scan(ctx, &existingPayload)
	if err == nil {
		var existing billing.CallLegUsageRecord
		if unmarshalErr := json.Unmarshal([]byte(existingPayload), &existing); unmarshalErr != nil {
			return fmt.Errorf("billingstore: decode existing call-leg usage: %w", unmarshalErr)
		}
		if replayErr := billing.CheckCallLegUsageReplay(existing, sealed); replayErr != nil {
			return replayErr
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("billingstore: commit call-leg usage replay: %w", err)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("billingstore: lookup call-leg usage: %w", err)
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		return fmt.Errorf("billingstore: encode call-leg usage: %w", err)
	}
	sealedAt := time.Now().UTC()
	// Pre-fix legacy rows have no sequence (attempt_seq NULL); corrected
	// records persist the exact positive attempt sequence explicitly.
	var attemptSeq any
	if sealed.AttemptSeq > 0 {
		attemptSeq = sealed.AttemptSeq
	}
	_, err = tx.NewRaw(`INSERT INTO usage_leg_records( usage_leg_key, fingerprint, call_id, a_leg_id, b_leg_id, attempt_seq, backend_id, provider_id, model_id, started_at, finished_at, outcome, surfaced, payload_json, sealed_at ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, sealed.Key, sealed.Fingerprint, sealed.CallID.String(), sealed.ALegID, sealed.BLegID, attemptSeq,
		sealed.BackendID, sealed.ProviderID, sealed.ModelID, sealed.StartedAt, sealed.FinishedAt,
		string(sealed.Outcome), string(sealed.Surfaced), string(payload), sealedAt).Exec(ctx)
	if err != nil {
		if isLegAttemptSeqConflict(err) {
			return fmt.Errorf("%w: %w", ErrLegAttemptSequenceConflict, err)
		}
		return fmt.Errorf("billingstore: insert call-leg usage: %w", err)
	}
	if _, err := tx.NewRaw(`INSERT INTO provider_cost_work(usage_leg_key, call_id, status, attempt_count, next_attempt_at, last_error, updated_at) VALUES (?, ?, 'pending', 0, ?, '', ?) ON CONFLICT(usage_leg_key) DO NOTHING`, sealed.Key, sealed.CallID.String(), sealedAt, sealedAt).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: enqueue provider cost work: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: commit call-leg usage: %w", err)
	}
	return nil
}

func (s *DurableStore) ListPendingProviderCostWork(ctx context.Context, limit int) ([]billing.ProviderCostWork, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("billingstore: nil store")
	}
	if limit <= 0 {
		limit = 32
	}
	if err := s.pruneProcessedProviderCostWork(ctx, time.Now().UTC().Add(-24*time.Hour)); err != nil {
		return nil, err
	}
	type workRow struct {
		CallID    string         `bun:"call_id"`
		AccountID sql.NullString `bun:"account_id"`
		Payload   string         `bun:"payload_json"`
	}
	var rows []workRow
	if err := s.db.NewRaw(`
SELECT w.call_id, c.account_id, l.payload_json
FROM provider_cost_work w
JOIN usage_leg_records l ON l.usage_leg_key = w.usage_leg_key
LEFT JOIN usage_call_records c ON c.call_id = w.call_id
WHERE w.status = 'pending' AND w.next_attempt_at <= ?
ORDER BY w.next_attempt_at, w.updated_at, w.usage_leg_key
LIMIT ?`, time.Now().UTC(), limit).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("billingstore: list pending provider cost work: %w", err)
	}
	out := make([]billing.ProviderCostWork, 0, len(rows))
	for _, row := range rows {
		callID, err := billing.ParseBillingCallID(row.CallID)
		if err != nil {
			return nil, err
		}
		var leg billing.CallLegUsageRecord
		if err := json.Unmarshal([]byte(row.Payload), &leg); err != nil {
			return nil, fmt.Errorf("billingstore: decode pending provider cost leg: %w", err)
		}
		if err := billing.CheckCallLegUsageReplay(leg, leg); err != nil {
			return nil, err
		}
		accountID := ""
		if row.AccountID.Valid {
			accountID = strings.TrimSpace(row.AccountID.String)
		}
		out = append(out, billing.ProviderCostWork{AccountID: accountID, CallID: callID, Leg: leg})
	}
	return out, nil
}

func (s *DurableStore) GetCallLegUsage(ctx context.Context, key string) (billing.CallLegUsageRecord, error) {
	if s == nil || s.db == nil {
		return billing.CallLegUsageRecord{}, fmt.Errorf("billingstore: nil store")
	}
	var payload string
	if err := s.db.NewRaw(`SELECT payload_json FROM usage_leg_records WHERE usage_leg_key = ?`, strings.TrimSpace(key)).Scan(ctx, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billing.CallLegUsageRecord{}, ErrUsageRecordNotFound
		}
		return billing.CallLegUsageRecord{}, err
	}
	var record billing.CallLegUsageRecord
	if err := json.Unmarshal([]byte(payload), &record); err != nil {
		return billing.CallLegUsageRecord{}, fmt.Errorf("billingstore: decode call-leg usage: %w", err)
	}
	if err := billing.CheckCallLegUsageReplay(record, record); err != nil {
		return billing.CallLegUsageRecord{}, err
	}
	return record, nil
}
