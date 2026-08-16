package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func (s *DurableStore) QueryOpenExposures(ctx context.Context, accountID string, page billing.PageRequest) (billing.ExposurePage, error) {
	page, err := page.Normalize()
	if err != nil {
		return billing.ExposurePage{}, err
	}
	accountID = strings.TrimSpace(accountID)
	afterKey := filterAfterKey(page)
	query := `SELECT e.exposure_key, e.account_id, e.call_id, e.max_exposure_nano, e.currency, e.pricing_ref, e.charge_policy_ref, e.fingerprint, e.balance_nano, e.credit_floor_nano, e.open_exposure_nano, e.settled_headroom_nano, e.safety_margin_before_nano, e.safety_margin_after_nano, e.status, e.created_at, e.closed_at FROM call_exposures e WHERE e.status = 'open' AND e.exposure_key > ?`
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
	callID, err := billing.ParseBillingCallID(strings.TrimSpace(callIDRaw))
	if err != nil {
		return billing.CallExplanation{}, fmt.Errorf("%w: %w", billing.ErrReportInvalid, err)
	}
	exposure, err := s.GetCallExposure(ctx, callID)
	if err != nil {
		if errors.Is(err, billing.ErrExposureNotFound) {
			return billing.CallExplanation{}, billing.ErrReportNotFound
		}
		return billing.CallExplanation{}, err
	}
	report := exposureReportFromExposure(exposure)
	var closure billing.CallUsageRecord
	closure, err = s.GetCallUsage(ctx, callID)
	if err != nil {
		if !errors.Is(err, ErrUsageRecordNotFound) {
			return billing.CallExplanation{}, err
		}
	} else {
		report.ALegID = closure.ALegID
		report.SessionID = closure.SessionID
	}
	legs, err := s.loadCallLegUsageByCall(ctx, s.db, callID)
	if err != nil {
		return billing.CallExplanation{}, err
	}
	customerOps, providerOps, err := s.loadCallOperationSnapshots(ctx, exposure.AccountID, callID, legs)
	if err != nil {
		return billing.CallExplanation{}, err
	}
	transactions, err := s.loadCallJournals(ctx, exposure.AccountID, callID.String())
	if err != nil {
		return billing.CallExplanation{}, err
	}
	integrity, err := s.readIntegrityReport(ctx, exposure.AccountID)
	if err != nil {
		return billing.CallExplanation{}, err
	}
	currency := exposure.Max.Currency
	revenue, cost, issues := billing.SummarizeJournalForReport(transactions, currency)
	integrity.Issues = append(integrity.Issues, issues...)
	margin, marginErr := billing.ReportMargin(currency, revenue, cost)
	if marginErr != nil {
		integrity.Issues = append(integrity.Issues, billing.ReconciliationIssue{Code: "margin_overflow", Detail: callID.String()})
	}
	if len(integrity.Issues) > 0 {
		integrity.OK = false
	}
	processed := false
	for _, op := range customerOps {
		if op.OperationKind == "customer_call_settlement" || op.OperationKind == "customer_no_charge_repair" {
			processed = true
			break
		}
	}
	return billing.CallExplanation{
		CallID: callID.String(), Exposure: report, Closure: closure, Legs: legs,
		CustomerOperations: customerOps, ProviderCostOperations: providerOps,
		Transactions: transactions, Reconciliation: &integrity,
		Result: billing.TurnResultSummary{
			CustomerCharge: billing.Money{Currency: currency, Nano: revenue},
			ProviderCost:   billing.Money{Currency: currency, Nano: cost},
			GrossMargin:    margin, Processed: processed,
		},
	}, nil
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

func (s *DurableStore) loadCallJournals(ctx context.Context, accountID, callID string) ([]billing.JournalTransaction, error) {
	var rows []journalTransactionRow
	if err := s.db.NewRaw(`SELECT transaction_id, account_id, book, currency, source_key, semantic_fingerprint, turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id, correction_group_id, operation_kind, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at FROM journal_transactions WHERE account_id = ? AND turn_id = ? ORDER BY account_sequence`, accountID, callID).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	return loadJournals(ctx, s.db, rows)
}

func (s *DurableStore) loadCallOperationSnapshots(ctx context.Context, accountID string, callID billing.BillingCallID, legs []billing.CallLegUsageRecord) ([]billing.OperationSnapshot, []billing.OperationSnapshot, error) {
	sourceKeys := []string{callID.String()}
	for _, leg := range legs {
		sourceKeys = append(sourceKeys, leg.Key)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(sourceKeys)), ",")
	args := make([]any, 0, len(sourceKeys)+1)
	args = append(args, accountID)
	for _, key := range sourceKeys {
		args = append(args, key)
	}
	var rows []operationSnapshotRow
	query := `SELECT operation_key, account_id, operation_kind, source_key, fingerprint, integrity_fingerprint, currency, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, account_sequence_start, account_sequence_end, created_at FROM billing_operation_snapshots WHERE account_id = ? AND source_key IN (` + placeholders + `) ORDER BY account_sequence_end, operation_key`
	if err := s.db.NewRaw(query, args...).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	customer := make([]billing.OperationSnapshot, 0)
	provider := make([]billing.OperationSnapshot, 0)
	for _, row := range rows {
		snap := billing.OperationSnapshot{
			OperationKey: row.OperationKey, OperationKind: row.OperationKind, SourceKey: row.SourceKey,
			Fingerprint: row.Fingerprint, Currency: row.Currency, Mode: billing.AccountMode(row.Mode),
			Before: billing.AccountSnapshot{
				BalanceNano: row.BalanceBefore, ReservedNano: row.ReservedBefore, SpendableNano: row.SpendableBefore,
				CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit, Mode: billing.AccountMode(row.Mode),
				Currency: row.Currency, Version: row.VersionBefore,
			},
			After: billing.AccountSnapshot{
				BalanceNano: row.BalanceAfter, ReservedNano: row.ReservedAfter, SpendableNano: row.SpendableAfter,
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
	return customer, provider, nil
}
