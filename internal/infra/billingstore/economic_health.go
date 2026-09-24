package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Task 16.3B durable economics health snapshot adapter.
//
// Every read is a bounded, store-scoped aggregate over existing indexes and
// worker-queue tables. Provider-only diagnostics never touch customer
// balance state: this file contains no account lock, no account read and no
// account-table access, so a supplier backlog cannot contend with customer
// admission. Free-text columns (retry reason, last error) are collapsed
// through the domain allowlists; raw strings never become identity. The
// reconciliation window is capped and retained payloads parse through the
// canonical retention validator, so corrupt rows fail closed instead of
// surfacing partial counters.

// EconomicHealthSnapshot aggregates the store's economics worker, statement
// and reconciliation state into one bounded point-in-time rollup.
func (s *DurableStore) EconomicHealthSnapshot(ctx context.Context) (billing.EconomicHealthSnapshot, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.EconomicHealthSnapshot{}, err
	}
	queues, retries, err := s.economicHealthQueueRows(ctx)
	if err != nil {
		return billing.EconomicHealthSnapshot{}, err
	}
	statements, err := s.economicHealthStatementRows(ctx)
	if err != nil {
		return billing.EconomicHealthSnapshot{}, err
	}
	retentions, capped, err := s.economicHealthRetentionWindow(ctx)
	if err != nil {
		return billing.EconomicHealthSnapshot{}, err
	}
	snapshot, err := billing.SummarizeEconomicHealth(billing.EconomicHealthInput{
		Queues: queues, Retries: retries, Statements: statements, Retentions: retentions,
	}, time.Now().UTC())
	if err != nil {
		return billing.EconomicHealthSnapshot{}, err
	}
	snapshot.WindowCapped = capped
	return snapshot, nil
}

// economicHealthNanosToSeconds converts the durable nanosecond worker
// timestamp to whole seconds for age math. A zero or negative value stays
// zero so a missing timestamp never fabricates an age.
func economicHealthNanosToSeconds(nanos int64) int64 {
	if nanos <= 0 {
		return 0
	}
	return nanos / int64(time.Second)
}

// economicHealthWorkBucket prefers the durable work kind (the three
// documented economic work kinds) and falls back to the worker queue for
// legacy rows whose kind is absent. Unknown values stay as-is here and are
// collapsed to "other" by the domain summarizer.
func economicHealthWorkBucket(queue, workKind string) string {
	if billing.EconomicWorkKind(workKind).Validate() == nil {
		return workKind
	}
	return queue
}

func (s *DurableStore) economicHealthQueueRows(ctx context.Context) ([]billing.EconomicQueueRow, []billing.EconomicRetryRow, error) {
	var queueRows []struct {
		Queue           string `bun:"queue"`
		WorkKind        string `bun:"work_kind"`
		Status          string `bun:"status"`
		Count           int    `bun:"row_count"`
		OldestCreatedNS int64  `bun:"oldest_created_ns"`
		MaxAttempts     int    `bun:"max_attempts"`
	}
	if err := s.db.NewRaw(`SELECT queue, work_kind, status, COUNT(1) AS row_count, `+
		`COALESCE(MIN(created_at_unix), 0) AS oldest_created_ns, COALESCE(MAX(attempt_count), 0) AS max_attempts `+
		`FROM billing_economic_revision_work_state WHERE store_id = ? GROUP BY queue, work_kind, status`,
		s.storeID).Scan(ctx, &queueRows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("billingstore: economics health queues: %w", err)
	}
	rows := make([]billing.EconomicQueueRow, 0, len(queueRows))
	for _, row := range queueRows {
		rows = append(rows, billing.EconomicQueueRow{
			Queue: economicHealthWorkBucket(row.Queue, row.WorkKind), Status: row.Status,
			Count: row.Count, OldestCreatedUnix: economicHealthNanosToSeconds(row.OldestCreatedNS),
			MaxAttempts: row.MaxAttempts,
		})
	}

	var retryRows []struct {
		Queue    string `bun:"queue"`
		WorkKind string `bun:"work_kind"`
		Reason   string `bun:"retry_reason"`
		Count    int    `bun:"row_count"`
		Attempts int    `bun:"max_attempts"`
	}
	if err := s.db.NewRaw(`SELECT queue, work_kind, retry_reason, COUNT(1) AS row_count, COALESCE(MAX(attempt_count), 0) AS max_attempts `+
		`FROM billing_economic_revision_work_state WHERE store_id = ? GROUP BY queue, work_kind, retry_reason`,
		s.storeID).Scan(ctx, &retryRows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("billingstore: economics health retries: %w", err)
	}
	retries := make([]billing.EconomicRetryRow, 0, len(retryRows))
	for _, row := range retryRows {
		retries = append(retries, billing.EconomicRetryRow{
			Queue: economicHealthWorkBucket(row.Queue, row.WorkKind), Reason: row.Reason,
			Count: row.Count, Attempts: row.Attempts,
		})
	}

	// Legacy provider-costing queue: status-only aggregate, no reason and no
	// integer timestamp, so it contributes counts without an age.
	var legacyRows []struct {
		Status      string `bun:"status"`
		Count       int    `bun:"row_count"`
		MaxAttempts int    `bun:"max_attempts"`
	}
	if err := s.db.NewRaw(`SELECT status, COUNT(1) AS row_count, COALESCE(MAX(attempt_count), 0) AS max_attempts `+
		`FROM provider_cost_work GROUP BY status`).Scan(ctx, &legacyRows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("billingstore: economics health legacy work: %w", err)
	}
	for _, row := range legacyRows {
		rows = append(rows, billing.EconomicQueueRow{
			Queue: billing.EconomicHealthQueueProviderLegacy, Status: row.Status,
			Count: row.Count, MaxAttempts: row.MaxAttempts,
		})
	}
	return rows, retries, nil
}

func (s *DurableStore) economicHealthStatementRows(ctx context.Context) ([]billing.EconomicStatementRow, error) {
	var rows []struct {
		Outcome string `bun:"outcome"`
		Count   int    `bun:"row_count"`
	}
	if err := s.db.NewRaw(`SELECT outcome, COUNT(1) AS row_count FROM billing_statement_lines WHERE store_id = ? GROUP BY outcome`,
		s.storeID).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("billingstore: economics health statements: %w", err)
	}
	out := make([]billing.EconomicStatementRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, billing.EconomicStatementRow{Outcome: row.Outcome, Count: row.Count})
	}
	return out, nil
}

// economicHealthRetentionWindow reads at most the bounded window of newest
// immutable retention revisions in deterministic order. The cap is applied
// in SQL so a large history cannot materialize unbounded rows.
func (s *DurableStore) economicHealthRetentionWindow(ctx context.Context) ([]billing.ReconciliationRetentionResult, bool, error) {
	limit := billing.EconomicHealthMaxWindowRows
	var rows []struct {
		ResultJSON string `bun:"result_json"`
	}
	if err := s.db.NewRaw(`SELECT result_json FROM billing_reconciliations `+
		`WHERE store_id = ? AND result_schema_version = ? `+
		`ORDER BY created_at_unix DESC, reconciliation_id DESC, reconciliation_version DESC, id DESC LIMIT ?`,
		s.storeID, ReconciliationRecordSchemaRetention, limit+1).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("billingstore: economics health retention window: %w", err)
	}
	capped := false
	if len(rows) > limit {
		rows = rows[:limit]
		capped = true
	}
	out := make([]billing.ReconciliationRetentionResult, 0, len(rows))
	for _, row := range rows {
		result, err := billing.ParseReconciliationRetentionResult([]byte(row.ResultJSON))
		if err != nil {
			return nil, false, fmt.Errorf("billingstore: economics health retention record mismatch")
		}
		out = append(out, result)
	}
	return out, capped, nil
}

var _ billing.EconomicHealthReader = (*DurableStore)(nil)
