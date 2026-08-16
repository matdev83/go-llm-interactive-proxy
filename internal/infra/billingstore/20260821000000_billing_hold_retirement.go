package billingstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/uptrace/bun"
)

// HoldRetirementMigrationName is the cutover marker for legacy authorization
// holds. The table is retained during rollout for evidence-preserving recovery;
// any account with an open legacy hold is blocked until an operator reconciles it.
const HoldRetirementMigrationName = "20260821000000"

func registerHoldRetirementMigration() {
	migrations.MustRegister(holdRetirementSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func holdRetirementSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing hold retirement: nil database")
	}
	var accountIDs []string
	if err := db.NewRaw(`SELECT DISTINCT account_id FROM authorization_holds WHERE status = 'open' ORDER BY account_id`).Scan(ctx, &accountIDs); err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("billing hold retirement: scan open holds: %w", err)
	}
	for _, accountID := range accountIDs {
		if _, err := db.NewRaw(`UPDATE billing_accounts SET state = 'reconcile_required', updated_at = CURRENT_TIMESTAMP WHERE account_id = ? AND state = 'ready'`, accountID).Exec(ctx); err != nil {
			return fmt.Errorf("billing hold retirement: block account %q: %w", accountID, err)
		}
	}
	return nil
}
