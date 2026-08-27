package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

// ReservedNanoZeroMigrationName clears residual reserved_nano after hold retirement.
const ReservedNanoZeroMigrationName = "20260826000000"

func registerReservedNanoZeroMigration() {
	migrations.MustRegister(reservedNanoZeroUp, func(context.Context, *bun.DB) error { return nil })
}

func reservedNanoZeroUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing reserved_nano zeroing: nil database")
	}
	var accountIDs []string
	if err := db.NewRaw(`SELECT account_id FROM billing_accounts WHERE reserved_nano <> 0 AND state = 'ready' ORDER BY account_id`).Scan(ctx, &accountIDs); err != nil {
		return fmt.Errorf("billing reserved_nano zeroing: list ready residue: %w", err)
	}
	for _, accountID := range accountIDs {
		eventKey := "reserved-nano-zero:v1:" + accountID
		if _, err := db.NewRaw(`INSERT INTO billing_reconciliation_events(event_key, account_id, from_state, to_state, first_mismatch_sequence, balance_nano, reserved_nano, spendable_nano, created_at)
SELECT ?, account_id, state, 'reconcile_required', 0, balance_nano, reserved_nano, balance_nano, CURRENT_TIMESTAMP
FROM billing_accounts WHERE account_id = ? AND NOT EXISTS (SELECT 1 FROM billing_reconciliation_events WHERE event_key = ?)`, eventKey, accountID, eventKey).Exec(ctx); err != nil {
			return fmt.Errorf("billing reserved_nano zeroing: audit account %q: %w", accountID, err)
		}
		if _, err := db.NewRaw(`UPDATE billing_accounts SET state = 'reconcile_required', updated_at = CURRENT_TIMESTAMP WHERE account_id = ? AND state = 'ready'`, accountID).Exec(ctx); err != nil {
			return fmt.Errorf("billing reserved_nano zeroing: block account %q: %w", accountID, err)
		}
	}
	if _, err := db.NewRaw(`UPDATE billing_accounts SET reserved_nano = 0, updated_at = CURRENT_TIMESTAMP WHERE reserved_nano <> 0`).Exec(ctx); err != nil {
		return fmt.Errorf("billing reserved_nano zeroing: clear reserved: %w", err)
	}
	return nil
}
