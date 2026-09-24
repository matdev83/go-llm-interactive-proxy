package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const ALegReportScopeMigrationName = "20260925000000"

const (
	alegReportCallScopeIndex   = "idx_usage_call_records_account_aleg_sealed"
	alegReportJournalTurnIndex = "idx_billing_journal_account_turn"
	alegReportLegScopeIndex    = "idx_usage_leg_records_aleg_call_bleg"
)

// registerALegReportScopeMigration adds read-only covering indexes for the
// rolling A-leg report's bounded access paths. Indexes only: no columns,
// constraints, data, or writer behavior change.
func registerALegReportScopeMigration() {
	migrations.MustRegister(aLegReportScopeSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func aLegReportScopeSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing A-leg report scope schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = []string{
			`CREATE INDEX IF NOT EXISTS ` + alegReportCallScopeIndex + ` ON usage_call_records(account_id, a_leg_id, sealed_at, call_id)`,
			`CREATE INDEX IF NOT EXISTS ` + alegReportJournalTurnIndex + ` ON journal_transactions(account_id, turn_id)`,
			`CREATE INDEX IF NOT EXISTS ` + alegReportLegScopeIndex + ` ON usage_leg_records(a_leg_id, call_id, b_leg_id)`,
		}
	case dialect.PG:
		statements = []string{
			`CREATE INDEX IF NOT EXISTS ` + alegReportCallScopeIndex + ` ON usage_call_records(account_id, a_leg_id, sealed_at, call_id)`,
			`CREATE INDEX IF NOT EXISTS ` + alegReportJournalTurnIndex + ` ON journal_transactions(account_id, turn_id)`,
			`CREATE INDEX IF NOT EXISTS ` + alegReportLegScopeIndex + ` ON usage_leg_records(a_leg_id, call_id, b_leg_id)`,
		}
	default:
		return fmt.Errorf("billing A-leg report scope schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing A-leg report scope DDL: %w", err)
		}
	}
	return nil
}
