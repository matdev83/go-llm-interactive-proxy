package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingReconciliationRetentionTenantIndexMigrationName adds the tenant-bearing
// retention index used by the tenant-refined latest read.
const BillingReconciliationRetentionTenantIndexMigrationName = "20260927000000"

const billingReconciliationRetentionTenantIndex = "idx_billing_reconciliations_retention_tenant_scope"

// registerBillingReconciliationRetentionTenantIndexMigration registers the
// index-only migration so tenant_id stays an index condition for
// subject+tenant latest reads instead of a post-index filter that could scan
// an unbounded subject history.
func registerBillingReconciliationRetentionTenantIndexMigration() {
	migrations.MustRegister(billingReconciliationRetentionTenantIndexUp, func(context.Context, *bun.DB) error { return nil })
}

func billingReconciliationRetentionTenantIndexUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing reconciliation retention tenant index: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite, dialect.PG:
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS `+billingReconciliationRetentionTenantIndex+` ON billing_reconciliations(store_id, result_schema_version, subject_kind, subject_id, tenant_id, created_at_unix, reconciliation_id, reconciliation_version, id)`); err != nil {
			return fmt.Errorf("billing reconciliation retention tenant index DDL: %w", err)
		}
	default:
		return fmt.Errorf("billing reconciliation retention tenant index: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	return nil
}
