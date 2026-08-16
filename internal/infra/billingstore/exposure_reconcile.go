package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func (s *DurableStore) ReconcileBillingAccount(ctx context.Context, accountID string) (billing.BillingReconciliationReport, error) {
	financial, financialErr := s.ReconcileAccount(ctx, accountID)
	if financialErr != nil && !errors.Is(financialErr, ErrReconciliationFailed) {
		return billing.BillingReconciliationReport{Financial: financial}, financialErr
	}
	exposure, exposureErr := s.ReconcileOpenExposure(ctx, accountID)
	if exposureErr != nil {
		return billing.BillingReconciliationReport{Financial: financial, Exposure: exposure}, exposureErr
	}
	if !financial.OK || !exposure.OK {
		if err := s.MarkAccountReconcileRequired(ctx, accountID); err != nil {
			return billing.BillingReconciliationReport{Financial: financial, Exposure: exposure}, err
		}
		return billing.BillingReconciliationReport{Financial: financial, Exposure: exposure}, ErrReconciliationFailed
	}
	financial, financialErr = s.ReconcileAccount(ctx, accountID)
	if financialErr != nil {
		return billing.BillingReconciliationReport{Financial: financial, Exposure: exposure}, financialErr
	}
	return billing.BillingReconciliationReport{Financial: financial, Exposure: exposure, OK: financial.OK && exposure.OK}, nil
}

func (s *DurableStore) ReconcileOpenExposure(ctx context.Context, accountID string) (billing.ExposureReconciliationReport, error) {
	if s == nil || s.db == nil {
		return billing.ExposureReconciliationReport{}, fmt.Errorf("billingstore: nil store")
	}
	accountID = strings.TrimSpace(accountID)
	account, err := s.GetAccount(ctx, accountID)
	if err != nil {
		return billing.ExposureReconciliationReport{}, err
	}
	var rows []exposureRow
	if err := s.db.NewRaw(`SELECT exposure_key, account_id, call_id, max_exposure_nano, currency, pricing_ref, charge_policy_ref, fingerprint, balance_nano, credit_floor_nano, open_exposure_nano, settled_headroom_nano, safety_margin_before_nano, safety_margin_after_nano, status, created_at, closed_at FROM call_exposures WHERE account_id = ? AND status = 'open' ORDER BY call_id`, accountID).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return billing.ExposureReconciliationReport{}, err
	}
	exposures := make([]billing.CallExposure, 0, len(rows))
	report := billing.ExposureReconciliationReport{AccountID: accountID, Currency: account.Currency, Rows: len(rows)}
	for _, row := range rows {
		exposure, decodeErr := exposureFromRow(row)
		if decodeErr != nil {
			report.Issues = append(report.Issues, billing.ReconciliationIssue{Code: "exposure_row_invalid", Detail: row.ExposureKey})
			continue
		}
		if exposure.AccountID != accountID || exposure.Max.Currency != account.Currency || exposure.Basis.OpenExposureNano < 0 {
			report.Issues = append(report.Issues, billing.ReconciliationIssue{Code: "exposure_scope_invalid", Detail: exposure.CallID})
			continue
		}
		exposures = append(exposures, exposure)
	}
	open, err := billing.OpenExposure(account.Currency, exposures)
	if err != nil {
		report.Issues = append(report.Issues, billing.ReconciliationIssue{Code: "exposure_sum_invalid", Detail: accountID})
	} else {
		report.Open = open
	}
	report.OK = len(report.Issues) == 0
	return report, nil
}

func (s *DurableStore) RepairExposureNoCharge(ctx context.Context, callID billing.BillingCallID, sourceKey string) (billing.CallSettlement, error) {
	if s == nil || s.db == nil {
		return billing.CallSettlement{}, fmt.Errorf("billingstore: nil store")
	}
	if err := callID.Validate(); err != nil {
		return billing.CallSettlement{}, err
	}
	if strings.TrimSpace(sourceKey) == "" {
		return billing.CallSettlement{}, fmt.Errorf("%w: repair source key is required", billing.ErrSettlementInvalid)
	}
	complete, err := s.ClaimCompleteCall(ctx, callID)
	if err != nil {
		return billing.CallSettlement{}, fmt.Errorf("%w: positive complete-call evidence: %w", billing.ErrCallIncomplete, err)
	}
	return s.applyNoChargeRepair(ctx, callID, sourceKey, complete)
}

func (s *DurableStore) RepairIncompleteCallNoCharge(ctx context.Context, callID billing.BillingCallID, sourceKey string) (billing.CallSettlement, error) {
	if s == nil || s.db == nil {
		return billing.CallSettlement{}, fmt.Errorf("billingstore: nil store")
	}
	if err := callID.Validate(); err != nil {
		return billing.CallSettlement{}, err
	}
	if strings.TrimSpace(sourceKey) == "" {
		return billing.CallSettlement{}, fmt.Errorf("%w: repair source key is required", billing.ErrSettlementInvalid)
	}
	closure, err := s.GetCallUsage(ctx, callID)
	if err != nil {
		if errors.Is(err, ErrUsageRecordNotFound) {
			return billing.CallSettlement{}, fmt.Errorf("%w: call closure is required", billing.ErrCallIncomplete)
		}
		return billing.CallSettlement{}, err
	}
	legs, err := s.ListCallLegUsage(ctx, callID)
	if err != nil {
		return billing.CallSettlement{}, err
	}
	present := make(map[string]struct{}, len(legs))
	for _, leg := range legs {
		present[strings.TrimSpace(leg.BLegID)] = struct{}{}
	}
	now := closure.FinishedAt
	if now.IsZero() {
		now = closure.StartedAt
	}
	for _, id := range closure.ExpectedBLegIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := present[id]; ok {
			continue
		}
		synthetic := billing.CallLegUsageRecord{
			CallID: callID, ALegID: closure.ALegID, BLegID: id,
			BackendID: "unknown", ProviderID: "unknown", ModelID: "unknown",
			StartedAt: now, FinishedAt: now, Outcome: billing.LegOutcomeNeverStarted, Surfaced: billing.SurfacedNo,
			Evidence: billing.FinalBillingEvidence{Source: billing.EvidenceSourceUnavailable, Authority: billing.EvidenceAuthorityUnavailable},
		}
		if err := s.AppendCallLegUsage(ctx, synthetic); err != nil {
			return billing.CallSettlement{}, err
		}
	}
	complete, err := s.ClaimCompleteCall(ctx, callID)
	if err != nil {
		return billing.CallSettlement{}, fmt.Errorf("%w: positive complete-call evidence: %w", billing.ErrCallIncomplete, err)
	}
	return s.applyNoChargeRepair(ctx, callID, sourceKey, complete)
}

func (s *DurableStore) applyNoChargeRepair(ctx context.Context, callID billing.BillingCallID, sourceKey string, complete billing.CompleteCall) (billing.CallSettlement, error) {
	exposure, err := s.GetCallExposure(ctx, callID)
	if err != nil {
		return billing.CallSettlement{}, err
	}
	fingerprint := "no-charge-repair:v1:" + strings.TrimSpace(sourceKey)
	settled, err := s.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: complete.Closure, Exposure: exposure,
		Result:        billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 0, Currency: exposure.Max.Currency}, Fingerprint: fingerprint},
		OperationKind: "customer_no_charge_repair",
	})
	if err != nil {
		_ = s.RetryCompleteCall(ctx, callID, "no_charge_repair")
		return billing.CallSettlement{}, err
	}
	return settled, nil
}

var _ billing.ExposureRecovery = (*DurableStore)(nil)
