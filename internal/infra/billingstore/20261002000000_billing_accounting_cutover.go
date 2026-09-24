package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingAccountingCutoverMigrationName identifies the durable per-store
// accounting cutover marker (Task 17.3A, Migration Strategy step 6
// foundation). One row per configured deployment/store boundary records the
// explicit accounting generation, monotonic version/epoch, active posting
// owner (V1/V2 writer lineage), and compatibility floor needed for later
// claim fencing (17.3B) and rollback checks (17.4). No terminal claim,
// worker, or admission wiring lives in this migration.
const BillingAccountingCutoverMigrationName = "20261002000000"

const billingAccountingCutoverStateIndex = "idx_billing_accounting_cutover_state"

func registerBillingAccountingCutoverMigration() {
	migrations.MustRegister(billingAccountingCutoverSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingAccountingCutoverSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing accounting cutover schema: nil database")
	}
	if ctx == nil {
		return fmt.Errorf("billing accounting cutover schema: nil context")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = []string{
			`CREATE TABLE IF NOT EXISTS billing_accounting_cutover (
				store_id TEXT NOT NULL PRIMARY KEY,
				generation INTEGER NOT NULL CHECK (generation IN (1, 2)),
				state TEXT NOT NULL CHECK (state IN ('v1_active', 'v2_shadow', 'v1_draining', 'v2_active')),
				active_posting_owner TEXT NOT NULL CHECK (active_posting_owner IN ('v1', 'v2')),
				compatibility_floor TEXT NOT NULL CHECK (compatibility_floor IN ('v1', 'v2')),
				version INTEGER NOT NULL CHECK (version > 0),
				epoch INTEGER NOT NULL CHECK (epoch > 0),
				transition_id TEXT NOT NULL CHECK (length(transition_id) > 0 AND length(transition_id) <= 128),
				created_at_unix INTEGER NOT NULL CHECK (created_at_unix > 0),
				updated_at_unix INTEGER NOT NULL CHECK (updated_at_unix > 0 AND updated_at_unix >= created_at_unix)
			)`,
			`CREATE INDEX IF NOT EXISTS ` + billingAccountingCutoverStateIndex + `
				ON billing_accounting_cutover(state)`,
		}
	case dialect.PG:
		statements = []string{
			`CREATE TABLE IF NOT EXISTS billing_accounting_cutover (
				store_id TEXT NOT NULL PRIMARY KEY,
				generation INTEGER NOT NULL CHECK (generation IN (1, 2)),
				state TEXT NOT NULL CHECK (state IN ('v1_active', 'v2_shadow', 'v1_draining', 'v2_active')),
				active_posting_owner TEXT NOT NULL CHECK (active_posting_owner IN ('v1', 'v2')),
				compatibility_floor TEXT NOT NULL CHECK (compatibility_floor IN ('v1', 'v2')),
				version BIGINT NOT NULL CHECK (version > 0),
				epoch BIGINT NOT NULL CHECK (epoch > 0),
				transition_id TEXT NOT NULL CHECK (char_length(transition_id) > 0 AND char_length(transition_id) <= 128),
				created_at_unix BIGINT NOT NULL CHECK (created_at_unix > 0),
				updated_at_unix BIGINT NOT NULL CHECK (updated_at_unix > 0 AND updated_at_unix >= created_at_unix)
			)`,
			`CREATE INDEX IF NOT EXISTS ` + billingAccountingCutoverStateIndex + `
				ON billing_accounting_cutover(state)`,
		}
	default:
		return fmt.Errorf("billing accounting cutover schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing accounting cutover DDL: %w", err)
		}
	}
	return nil
}
