package billingstore

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func (s *DurableStore) QueryReconcileRequired(ctx context.Context, page billing.PageRequest) (billing.AccountStatePage, error) {
	page, err := page.Normalize()
	if err != nil {
		return billing.AccountStatePage{}, err
	}
	var rows []accountRow
	if err := s.db.NewRaw(`SELECT account_id, currency, mode, credit_limit_nano, balance_nano, opening_balance_nano, version, state, created_at, updated_at FROM billing_accounts WHERE account_id > ? AND state = 'reconcile_required' ORDER BY account_id LIMIT ?`, strings.TrimSpace(page.AfterKey), page.Limit+1).Scan(ctx, &rows); err != nil {
		return billing.AccountStatePage{}, err
	}
	next := ""
	if len(rows) > page.Limit {
		rows = rows[:page.Limit]
		next = rows[len(rows)-1].ID
	}
	items := make([]billing.Account, 0, len(rows))
	for _, row := range rows {
		items = append(items, billing.Account{ID: row.ID, Currency: row.Currency, Mode: billing.AccountMode(row.Mode), CreditLimit: row.CreditLimit, BalanceNano: row.Balance, Version: row.Version, State: billing.AccountState(row.State)})
	}
	return billing.AccountStatePage{Items: items, NextCursor: next}, nil
}

func filterAfterKey(page billing.PageRequest) string { return strings.TrimSpace(page.AfterKey) }
