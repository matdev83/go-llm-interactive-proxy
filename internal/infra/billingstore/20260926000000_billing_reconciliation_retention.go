package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingReconciliationRetentionMigrationName identifies the additive durable
// full-result reconciliation retention envelope version marker and the
// scope-ordered read index for Task 12.3B.
const BillingReconciliationRetentionMigrationName = "20260926000000"

const billingReconciliationRetentionIndex = "idx_billing_reconciliations_retention_scope"

// registerBillingReconciliationRetentionMigration adds the durable
// reconciliation retention schema. The existing billing_reconciliations table
// already stores the canonical envelope; this migration distinguishes the
// full-result retention generation and indexes its bounded latest/list reads.
func registerBillingReconciliationRetentionMigration() {
	migrations.MustRegister(billingReconciliationRetentionSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingReconciliationRetentionSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing reconciliation retention schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		var tableCount int
		if err := db.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'billing_reconciliations'`).Scan(ctx, &tableCount); err != nil {
			return fmt.Errorf("billing reconciliation retention SQLite table probe: %w", err)
		}
		if tableCount > 0 {
			var columnCount int
			if err := db.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('billing_reconciliations') WHERE name = 'result_schema_version'`).Scan(ctx, &columnCount); err != nil {
				return fmt.Errorf("billing reconciliation retention SQLite column probe: %w", err)
			}
			if columnCount == 0 {
				if _, err := db.ExecContext(ctx, `ALTER TABLE billing_reconciliations ADD COLUMN result_schema_version INTEGER NOT NULL DEFAULT 1`); err != nil {
					return fmt.Errorf("billing reconciliation retention SQLite column: %w", err)
				}
			}
		}
		statements = []string{
			`CREATE INDEX IF NOT EXISTS ` + billingReconciliationRetentionIndex + ` ON billing_reconciliations(store_id, result_schema_version, subject_kind, subject_id, created_at_unix, reconciliation_id, reconciliation_version, id)`,
		}
	case dialect.PG:
		statements = []string{
			`ALTER TABLE IF EXISTS billing_reconciliations ADD COLUMN IF NOT EXISTS result_schema_version INTEGER NOT NULL DEFAULT 1`,
			`CREATE INDEX IF NOT EXISTS ` + billingReconciliationRetentionIndex + ` ON billing_reconciliations(store_id, result_schema_version, subject_kind, subject_id, created_at_unix, reconciliation_id, reconciliation_version, id)`,
		}
	default:
		return fmt.Errorf("billing reconciliation retention schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing reconciliation retention DDL: %w", err)
		}
	}
	return nil
}
