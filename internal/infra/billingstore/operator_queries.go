package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// QueryProcessing returns bounded mutable processing metadata. Sealed TUR/LUR
// payloads are deliberately not included in this operator queue view.
func (s *DurableStore) QueryProcessing(ctx context.Context, filter billing.ReportFilter) (billing.ProcessingPage, error) {
	filter, err := filter.Normalize()
	if err != nil {
		return billing.ProcessingPage{}, err
	}
	query := `SELECT p.tur_key, p.tur_fingerprint, p.status, p.lease_owner, p.lease_until, p.retry_count, p.safe_error_code, p.result_ref, p.updated_at FROM usage_record_processing p`
	if filter.AccountID != "" {
		query += ` JOIN turn_usage_records t ON t.tur_key = p.tur_key`
	}
	query += ` WHERE p.tur_key > ?`
	afterKey := filter.AfterKey
	if afterKey == "" {
		afterKey = filter.Page.AfterKey
	}
	args := []any{afterKey}
	if filter.AccountID != "" {
		query += ` AND t.account_id = ?`
		args = append(args, filter.AccountID)
	}
	if filter.Status != "" {
		if !filter.Status.Valid() {
			return billing.ProcessingPage{}, fmt.Errorf("%w: unsupported processing status", billing.ErrReportInvalid)
		}
		query += ` AND p.status = ?`
		args = append(args, string(filter.Status))
	} else {
		query += ` AND p.status IN ('pending','processing','retryable','unreconciled_cost','terminal_error')`
	}
	query += ` ORDER BY p.tur_key LIMIT ?`
	args = append(args, filter.Page.Limit+1)
	var rows []usageRecordProcessingRow
	if err := s.db.NewRaw(query, args...).Scan(ctx, &rows); err != nil {
		return billing.ProcessingPage{}, err
	}
	next := ""
	if len(rows) > filter.Page.Limit {
		rows = rows[:filter.Page.Limit]
		if len(rows) > 0 {
			next = rows[len(rows)-1].TURKey
		}
	}
	items := make([]billing.UsageRecordProcessing, 0, len(rows))
	for _, row := range rows {
		items = append(items, processingFromRow(row))
	}
	return billing.ProcessingPage{Items: items, NextCursor: next}, nil
}

// QueryOpenHolds returns bounded open authorization exposure for operators.
func (s *DurableStore) QueryOpenHolds(ctx context.Context, accountID string, page billing.PageRequest) (billing.HoldPage, error) {
	page, err := page.Normalize()
	if err != nil {
		return billing.HoldPage{}, err
	}
	accountID = strings.TrimSpace(accountID)
	query := `SELECT hold_key, authorization_id, account_id, tur_key, currency, amount_nano, status, pricing_ref, charge_policy_ref, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, closed_reason, released_amount_nano, closed_source_key, closed_fingerprint, closed_amount_nano, expires_at, created_at, closed_at FROM authorization_holds WHERE hold_key > ? AND status = 'open'`
	args := []any{filterAfterKey(page)}
	if accountID != "" {
		query += ` AND account_id = ?`
		args = append(args, accountID)
	}
	query += ` ORDER BY hold_key LIMIT ?`
	args = append(args, page.Limit+1)
	var rows []authorizationHoldRow
	if err := s.db.NewRaw(query, args...).Scan(ctx, &rows); err != nil {
		return billing.HoldPage{}, err
	}
	next := ""
	if len(rows) > page.Limit {
		rows = rows[:page.Limit]
		if len(rows) > 0 {
			next = rows[len(rows)-1].HoldKey
		}
	}
	items := make([]billing.HoldReport, 0, len(rows))
	for _, row := range rows {
		items = append(items, billing.HoldReport{ID: row.AuthorizationID, AccountID: row.AccountID, TURKey: row.TURKey, Amount: billing.Money{Nano: row.AmountNano, Currency: row.Currency}, Status: row.Status, ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt, ClosedReason: billing.ReleaseReason(row.ClosedReason), ReleasedAmount: billing.Money{Nano: row.ReleasedAmount, Currency: row.Currency}})
	}
	return billing.HoldPage{Items: items, NextCursor: next}, nil
}

// QueryReconcileRequired returns bounded fail-closed account state for repair
// operators. It is read-only and never clears the safety state.
func (s *DurableStore) QueryReconcileRequired(ctx context.Context, page billing.PageRequest) (billing.AccountStatePage, error) {
	page, err := page.Normalize()
	if err != nil {
		return billing.AccountStatePage{}, err
	}
	var rows []accountRow
	if err := s.db.NewRaw(`SELECT account_id, currency, mode, credit_limit_nano, balance_nano, opening_balance_nano, reserved_nano, version, state, created_at, updated_at FROM billing_accounts WHERE account_id > ? AND state = 'reconcile_required' ORDER BY account_id LIMIT ?`, filterAfterKey(page), page.Limit+1).Scan(ctx, &rows); err != nil {
		return billing.AccountStatePage{}, err
	}
	next := ""
	if len(rows) > page.Limit {
		rows = rows[:page.Limit]
		if len(rows) > 0 {
			next = rows[len(rows)-1].ID
		}
	}
	items := make([]billing.Account, 0, len(rows))
	for _, row := range rows {
		items = append(items, billing.Account{ID: row.ID, Currency: row.Currency, Mode: billing.AccountMode(row.Mode), CreditLimit: row.CreditLimit, BalanceNano: row.Balance, ReservedNano: row.Reserved, Version: row.Version, State: billing.AccountState(row.State)})
	}
	return billing.AccountStatePage{Items: items, NextCursor: next}, nil
}

func filterAfterKey(page billing.PageRequest) string { return strings.TrimSpace(page.AfterKey) }
