package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingProviderCostExecutionFenceMigrationName identifies the execution
// level provider-cost writer gate. Per-head posting fences remain responsible
// for selected amounts; this row only decides whether legacy aggregate work or
// V2 revision work owns the B-leg execution.
const BillingProviderCostExecutionFenceMigrationName = "20260923000000"

const billingProviderCostExecutionFenceIndex = "idx_billing_provider_cost_execution_fences_scope"

func registerBillingProviderCostExecutionFenceMigration() {
	migrations.MustRegister(billingProviderCostExecutionFenceSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingProviderCostExecutionFenceSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing provider cost execution fence schema: nil database")
	}
	if ctx == nil {
		return fmt.Errorf("billing provider cost execution fence schema: nil context")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = []string{
			`CREATE TABLE IF NOT EXISTS billing_provider_cost_execution_fences (
				id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
				store_id TEXT NOT NULL,
				account_id TEXT NOT NULL,
				call_id TEXT NOT NULL,
				execution_lineage_key TEXT NOT NULL,
				authority TEXT NOT NULL CHECK (authority IN ('legacy', 'revision')),
				owner_subject_kind TEXT NOT NULL,
				owner_head_key TEXT NOT NULL,
				owner_revision INTEGER NOT NULL CHECK (owner_revision > 0),
				owner_input_set_hash TEXT NOT NULL,
				owner_fingerprint TEXT NOT NULL,
				fence INTEGER NOT NULL CHECK (fence > 0),
				last_operation_key TEXT NOT NULL,
				last_transaction_id TEXT NOT NULL DEFAULT '',
				created_at_unix INTEGER NOT NULL,
				updated_at_unix INTEGER NOT NULL,
				UNIQUE(store_id, account_id, call_id, execution_lineage_key),
				FOREIGN KEY(account_id) REFERENCES billing_accounts
			)`,
			`CREATE INDEX IF NOT EXISTS ` + billingProviderCostExecutionFenceIndex + `
				ON billing_provider_cost_execution_fences(store_id, account_id, call_id, execution_lineage_key, authority)`,
		}
	case dialect.PG:
		statements = []string{
			`CREATE TABLE IF NOT EXISTS billing_provider_cost_execution_fences (
				id BIGSERIAL PRIMARY KEY,
				store_id TEXT NOT NULL,
				account_id TEXT NOT NULL,
				call_id TEXT NOT NULL,
				execution_lineage_key TEXT NOT NULL,
				authority TEXT NOT NULL CHECK (authority IN ('legacy', 'revision')),
				owner_subject_kind TEXT NOT NULL,
				owner_head_key TEXT NOT NULL,
				owner_revision BIGINT NOT NULL CHECK (owner_revision > 0),
				owner_input_set_hash TEXT NOT NULL,
				owner_fingerprint TEXT NOT NULL,
				fence BIGINT NOT NULL CHECK (fence > 0),
				last_operation_key TEXT NOT NULL,
				last_transaction_id TEXT NOT NULL DEFAULT '',
				created_at_unix BIGINT NOT NULL,
				updated_at_unix BIGINT NOT NULL,
				UNIQUE(store_id, account_id, call_id, execution_lineage_key),
				FOREIGN KEY(account_id) REFERENCES billing_accounts
			)`,
			`CREATE INDEX IF NOT EXISTS ` + billingProviderCostExecutionFenceIndex + `
				ON billing_provider_cost_execution_fences(store_id, account_id, call_id, execution_lineage_key, authority)`,
		}
	default:
		return fmt.Errorf("billing provider cost execution fence schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing provider cost execution fence DDL: %w", err)
		}
	}
	return nil
}
