package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
)

func (s *DurableStore) QueryOpenExposures(ctx context.Context, accountID string, page billing.PageRequest) (billing.ExposurePage, error) {
	page, err := page.Normalize()
	if err != nil {
		return billing.ExposurePage{}, err
	}
	accountID = strings.TrimSpace(accountID)
	afterKey := filterAfterKey(page)
	query := `SELECT e.exposure_key, e.account_id, e.call_id, e.max_exposure_nano, e.currency, e.pricing_ref, e.charge_policy_ref, e.route_tariffs, e.fingerprint, e.balance_nano, e.credit_floor_nano, e.open_exposure_nano, e.settled_headroom_nano, e.safety_margin_before_nano, e.safety_margin_after_nano, e.status, e.created_at, e.closed_at FROM call_exposures e WHERE e.status = 'open' AND e.exposure_key > ?`
	args := []any{afterKey}
	if accountID != "" {
		query += ` AND e.account_id = ?`
		args = append(args, accountID)
	}
	query += ` ORDER BY e.exposure_key LIMIT ?`
	args = append(args, page.Limit+1)
	var rows []exposureRow
	if err := s.db.NewRaw(query, args...).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return billing.ExposurePage{}, err
	}
	next := ""
	if len(rows) > page.Limit {
		rows = rows[:page.Limit]
		if len(rows) > 0 {
			next = exposureKey(rows[len(rows)-1].AccountID, rows[len(rows)-1].CallID)
		}
	}
	items := make([]billing.ExposureReport, 0, len(rows))
	for _, row := range rows {
		report, decodeErr := exposureReportFromRow(ctx, s, row)
		if decodeErr != nil {
			return billing.ExposurePage{}, decodeErr
		}
		items = append(items, report)
	}
	return billing.ExposurePage{Items: items, NextCursor: next}, nil
}

func (s *DurableStore) CallExplanation(ctx context.Context, callIDRaw string) (billing.CallExplanation, error) {
	data, err := s.loadCallExplanationTx(ctx, s.db, callIDRaw, true)
	if err != nil {
		return billing.CallExplanation{}, err
	}
	if !data.HasExposure {
		return billing.CallExplanation{}, billing.ErrReportNotFound
	}
	report := exposureReportFromExposure(data.Exposure)
	closure := data.Closure
	if data.HasClosure {
		report.ALegID = closure.ALegID
		report.SessionID = closure.SessionID
	}
	integrity, err := s.readIntegrityReport(ctx, data.Exposure.AccountID)
	if err != nil {
		return billing.CallExplanation{}, err
	}
	currency := data.Exposure.Max.Currency
	revenue, cost, issues := billing.SummarizeJournalForReport(data.Transactions, currency)
	integrity.Issues = append(integrity.Issues, issues...)
	margin, marginErr := billing.ReportMargin(currency, revenue, cost)
	if marginErr != nil {
		integrity.Issues = append(integrity.Issues, billing.ReconciliationIssue{Code: "margin_overflow", Detail: data.CallID.String()})
	}
	if len(integrity.Issues) > 0 {
		integrity.OK = false
	}
	processed := false
	for _, op := range data.CustomerOps {
		if op.OperationKind == "customer_call_settlement" || op.OperationKind == "customer_no_charge_repair" {
			processed = true
			break
		}
	}
	return billing.CallExplanation{
		CallID: data.CallID.String(), Exposure: report, Closure: closure, Legs: data.Legs,
		CustomerOperations: data.CustomerOps, ProviderCostOperations: data.ProviderOps,
		Transactions: data.Transactions, Reconciliation: &integrity,
		Result: billing.TurnResultSummary{
			CustomerCharge: billing.Money{Currency: currency, Nano: revenue},
			ProviderCost:   billing.Money{Currency: currency, Nano: cost},
			GrossMargin:    margin, Processed: processed,
		},
	}, nil
}

// callExplanationData is the durable per-call fact set behind CallExplanation
// and the rolling A-leg snapshot. Every row comes from the caller's query
// handle, so a report transaction observes one consistent snapshot instead of
// fanning out to per-call public queries on separate transactions.
type callExplanationData struct {
	CallID       billing.BillingCallID
	Exposure     billing.CallExposure
	HasExposure  bool
	Closure      billing.CallUsageRecord
	HasClosure   bool
	Legs         []billing.CallLegUsageRecord
	CustomerOps  []billing.OperationSnapshot
	ProviderOps  []billing.OperationSnapshot
	Transactions []billing.JournalTransaction
}

// loadCallExplanationTx loads one call's durable facts through q. When
// requireExposure is set, a missing exposure fails fast with
// ErrReportNotFound before any dependent read, preserving the public
// absence precedence; malformed exposure rows still fail as decode errors.
// Otherwise missing exposure or closure is reported with
// HasExposure/HasClosure rather than an error, so rolling snapshots can
// classify unexposed calls as pending lineage. Only malformed input,
// infrastructure failures, and undecodable rows fail.
func (s *DurableStore) loadCallExplanationTx(ctx context.Context, q bun.IDB, callIDRaw string, requireExposure bool) (callExplanationData, error) {
	callID, err := billing.ParseBillingCallID(strings.TrimSpace(callIDRaw))
	if err != nil {
		return callExplanationData{}, fmt.Errorf("%w: %w", billing.ErrReportInvalid, err)
	}
	var data callExplanationData
	data.CallID = callID
	var exposureRow exposureRow
	if err := q.NewRaw(`SELECT exposure_key, account_id, call_id, max_exposure_nano, currency, pricing_ref, charge_policy_ref, route_tariffs, fingerprint, balance_nano, credit_floor_nano, open_exposure_nano, settled_headroom_nano, safety_margin_before_nano, safety_margin_after_nano, status, created_at, closed_at FROM call_exposures WHERE call_id = ?`, callID.String()).Scan(ctx, &exposureRow); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return callExplanationData{}, err
		}
	} else {
		exposure, err := exposureFromRow(exposureRow)
		if err != nil {
			return callExplanationData{}, err
		}
		data.Exposure = exposure
		data.HasExposure = true
	}
	if !data.HasExposure && requireExposure {
		return callExplanationData{}, billing.ErrReportNotFound
	}
	closure, err := s.loadCallUsage(ctx, q, callID)
	if err != nil {
		if !errors.Is(err, ErrUsageRecordNotFound) {
			return callExplanationData{}, err
		}
	} else {
		data.Closure = closure
		data.HasClosure = true
	}
	accountID := ""
	if data.HasExposure {
		accountID = data.Exposure.AccountID
	} else if data.HasClosure {
		accountID = data.Closure.AccountID
	}
	legs, err := s.loadCallLegUsageByCall(ctx, q, callID)
	if err != nil {
		return callExplanationData{}, err
	}
	data.Legs = legs
	if accountID != "" {
		customerOps, providerOps, err := loadCallOperationSnapshots(ctx, q, accountID, callID, legs)
		if err != nil {
			return callExplanationData{}, err
		}
		data.CustomerOps = customerOps
		data.ProviderOps = providerOps
		transactions, err := loadCallJournals(ctx, q, accountID, callID.String())
		if err != nil {
			return callExplanationData{}, err
		}
		data.Transactions = transactions
	}
	return data, nil
}

func exposureReportFromRow(ctx context.Context, s *DurableStore, row exposureRow) (billing.ExposureReport, error) {
	exposure, err := exposureFromRow(row)
	if err != nil {
		return billing.ExposureReport{}, err
	}
	report := exposureReportFromExposure(exposure)
	callID, parseErr := billing.ParseBillingCallID(exposure.CallID)
	if parseErr == nil {
		if closure, getErr := s.GetCallUsage(ctx, callID); getErr == nil {
			report.ALegID = closure.ALegID
			report.SessionID = closure.SessionID
		} else if !errors.Is(getErr, ErrUsageRecordNotFound) {
			return billing.ExposureReport{}, getErr
		}
	}
	return report, nil
}

func exposureReportFromExposure(exposure billing.CallExposure) billing.ExposureReport {
	return billing.ExposureReport{
		AccountID: exposure.AccountID, CallID: exposure.CallID, Status: exposure.Status,
		Max: exposure.Max, PricingRef: exposure.PricingRef, ChargePolicyRef: exposure.ChargePolicyRef,
		Fingerprint: exposure.Fingerprint, CreatedAt: exposure.CreatedAt, ClosedAt: exposure.ClosedAt,
		Basis: exposure.Basis,
	}
}

func loadCallJournals(ctx context.Context, q bun.IDB, accountID, callID string) ([]billing.JournalTransaction, error) {
	rows, err := loadCallJournalRows(ctx, q, accountID, callID, 0)
	if err != nil {
		return nil, err
	}
	return loadJournals(ctx, q, rows)
}

// loadCallJournalRows reads the financial journal rows of one call in the
// canonical deterministic order. limit > 0 adds a database-side LIMIT used by
// bounded callers; limit <= 0 preserves the unbounded generic report read used
// by CallExplanation.
func loadCallJournalRows(ctx context.Context, q bun.IDB, accountID, callID string, limit int) ([]journalTransactionRow, error) {
	query := `SELECT transaction_id, account_id, book, currency, source_key, semantic_fingerprint, turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id, correction_group_id, operation_kind, balance_before_nano, balance_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at FROM journal_transactions WHERE account_id = ? AND turn_id = ? AND book = 'financial'` + journalOrderClause("")
	args := []any{accountID, callID}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	var rows []journalTransactionRow
	if err := q.NewRaw(query, args...).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func loadCallOperationSnapshots(ctx context.Context, q bun.IDB, accountID string, callID billing.BillingCallID, legs []billing.CallLegUsageRecord) ([]billing.OperationSnapshot, []billing.OperationSnapshot, error) {
	rows, err := loadCallOperationSnapshotRows(ctx, q, accountID, callID, legs, 0)
	if err != nil {
		return nil, nil, err
	}
	customer, provider := operationSnapshotsFromRows(rows)
	return customer, provider, nil
}

// loadCallOperationSnapshotRows reads the operation snapshots of one call and
// its legs in the canonical deterministic order. limit > 0 adds a
// database-side LIMIT used by bounded callers; limit <= 0 preserves the
// unbounded generic report read used by CallExplanation.
func loadCallOperationSnapshotRows(ctx context.Context, q bun.IDB, accountID string, callID billing.BillingCallID, legs []billing.CallLegUsageRecord, limit int) ([]operationSnapshotRow, error) {
	sourceKeys := []string{callID.String()}
	for _, leg := range legs {
		sourceKeys = append(sourceKeys, leg.Key)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(sourceKeys)), ",")
	args := make([]any, 0, len(sourceKeys)+2)
	args = append(args, accountID)
	for _, key := range sourceKeys {
		args = append(args, key)
	}
	query := `SELECT operation_key, account_id, operation_kind, source_key, fingerprint, integrity_fingerprint, currency, mode, balance_before_nano, balance_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, account_sequence_start, account_sequence_end, created_at FROM billing_operation_snapshots WHERE account_id = ? AND source_key IN (` + placeholders + `)` + operationSnapshotOrderClause
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	var rows []operationSnapshotRow
	if err := q.NewRaw(query, args...).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return rows, nil
}

// operationSnapshotsFromRows adapts durable snapshot rows into the canonical
// customer/provider operation sets. Kinds outside the settlement vocabulary
// are ignored, exactly as before; every row still counts against the caller's
// row budget before this projection runs.
func operationSnapshotsFromRows(rows []operationSnapshotRow) ([]billing.OperationSnapshot, []billing.OperationSnapshot) {
	customer := make([]billing.OperationSnapshot, 0)
	provider := make([]billing.OperationSnapshot, 0)
	for _, row := range rows {
		snap := billing.OperationSnapshot{
			OperationKey: row.OperationKey, OperationKind: row.OperationKind, SourceKey: row.SourceKey,
			Fingerprint: row.Fingerprint, Currency: row.Currency, Mode: billing.AccountMode(row.Mode),
			Before: billing.AccountSnapshot{
				BalanceNano: row.BalanceBefore, SpendableNano: row.SpendableBefore,
				CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit, Mode: billing.AccountMode(row.Mode),
				Currency: row.Currency, Version: row.VersionBefore,
			},
			After: billing.AccountSnapshot{
				BalanceNano: row.BalanceAfter, SpendableNano: row.SpendableAfter,
				CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit, Mode: billing.AccountMode(row.Mode),
				Currency: row.Currency, Version: row.VersionAfter,
			},
			SequenceStart: row.SequenceStart, SequenceEnd: row.SequenceEnd, CreatedAt: row.CreatedAt,
		}
		switch row.OperationKind {
		case "customer_call_settlement", "customer_no_charge_repair":
			customer = append(customer, snap)
		case "provider_call_cogs", "provider_cost_unreconciled":
			provider = append(provider, snap)
		}
	}
	return customer, provider
}
