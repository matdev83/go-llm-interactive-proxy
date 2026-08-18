package billingstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func (s *DurableStore) readIntegrityReport(ctx context.Context, accountID string) (billing.ReconciliationReport, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return billing.ReconciliationReport{}, err
	}
	defer func() { _ = tx.Rollback() }()
	account, err := getAccountTx(ctx, tx, accountID)
	if err != nil {
		return billing.ReconciliationReport{}, err
	}
	var opening struct {
		Balance     int64  `bun:"opening_balance_nano"`
		Currency    string `bun:"currency"`
		Mode        string `bun:"mode"`
		CreditLimit int64  `bun:"credit_limit_nano"`
		Fingerprint string `bun:"fingerprint"`
	}
	if err := tx.NewRaw(`SELECT opening_balance_nano, currency, mode, credit_limit_nano, fingerprint FROM billing_account_openings WHERE account_id = ?`, accountID).Scan(ctx, &opening); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billing.ReconciliationReport{AccountID: accountID, Issues: []billing.ReconciliationIssue{{Code: "opening_evidence_missing", Detail: accountID}}}, nil
		}
		return billing.ReconciliationReport{}, err
	}
	if opening.Fingerprint == "" {
		return billing.ReconciliationReport{AccountID: accountID, Issues: []billing.ReconciliationIssue{{Code: "opening_evidence_missing", Detail: accountID}}}, nil
	}
	journals, err := loadAllJournals(ctx, tx, accountID)
	if err != nil {
		return billing.ReconciliationReport{}, err
	}
	report := billing.ReplayAccount(account, opening.Balance, journals)
	if report.Rebuilt.Currency != account.Currency || report.Rebuilt.Mode != account.Mode || report.Rebuilt.CreditLimitNano != account.CreditLimit {
		report.AddIssue("materialized_policy_mismatch", 0, accountID)
	}
	if report.Rebuilt.BalanceNano != account.BalanceNano || report.Rebuilt.SpendableNano != mustSpendable(account) {
		report.AddIssue("materialized_state_mismatch", 0, accountID)
	}
	validateSettlementSnapshots(ctx, tx, accountID, journals, true, &report)
	return report, nil
}
