package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
)

func getAccountTx(ctx context.Context, q bun.IDB, accountID string) (billing.Account, error) {
	account, err := loadAccountRow(ctx, q, accountID)
	if err != nil {
		return billing.Account{}, err
	}
	if err := account.Validate(); err != nil {
		return billing.Account{}, err
	}
	return account, nil
}

func getAccountForReconcileTx(ctx context.Context, q bun.IDB, accountID string) (billing.Account, error) {
	account, err := loadAccountRow(ctx, q, accountID)
	if err != nil {
		return billing.Account{}, err
	}
	if err := account.Validate(); err != nil {
		return billing.Account{}, err
	}
	return account, nil
}

func loadAccountRow(ctx context.Context, q bun.IDB, accountID string) (billing.Account, error) {
	var row accountRow
	err := q.NewRaw(`SELECT account_id, currency, mode, credit_limit_nano, balance_nano, opening_balance_nano, version, state FROM billing_accounts WHERE account_id = ?`, accountID).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return billing.Account{}, ErrAccountNotFound
	}
	if err != nil {
		return billing.Account{}, fmt.Errorf("billingstore: read account: %w", err)
	}
	return billing.Account{ID: row.ID, Currency: row.Currency, Mode: billing.AccountMode(row.Mode), CreditLimit: row.CreditLimit, BalanceNano: row.Balance, Version: row.Version, State: billing.AccountState(row.State)}, nil
}
