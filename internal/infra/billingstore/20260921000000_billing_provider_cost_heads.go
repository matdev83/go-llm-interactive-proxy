package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingProviderCostHeadsMigrationName identifies the additive current-head
// projection used by incremental operator/provider-cost accrual.
const BillingProviderCostHeadsMigrationName = "20260921000000"

const billingProviderCostHeadIndex = "idx_billing_provider_cost_heads_scope"

func registerBillingProviderCostHeadsMigration() {
	migrations.MustRegister(billingProviderCostHeadsSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingProviderCostHeadsSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing provider cost heads schema: nil database")
	}
	if ctx == nil {
		return fmt.Errorf("billing provider cost heads schema: nil context")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = []string{
			`CREATE TABLE IF NOT EXISTS billing_provider_cost_heads (
				id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
				store_id TEXT NOT NULL,
				account_id TEXT NOT NULL,
				call_id TEXT NOT NULL,
				head_key TEXT NOT NULL,
				subject_kind TEXT NOT NULL,
				subject_id TEXT NOT NULL,
				subject_json TEXT NOT NULL,
				evidence_revision INTEGER NOT NULL CHECK (evidence_revision > 0),
				input_set_hash TEXT NOT NULL,
				valuation_id TEXT NOT NULL,
				amount_nano INTEGER NOT NULL CHECK (amount_nano >= 0),
				currency TEXT NOT NULL,
				head_version INTEGER NOT NULL CHECK (head_version > 0),
				fence INTEGER NOT NULL CHECK (fence > 0),
				last_operation_key TEXT NOT NULL DEFAULT '',
				last_transaction_id TEXT NOT NULL DEFAULT '',
				created_at_unix INTEGER NOT NULL,
				updated_at_unix INTEGER NOT NULL,
				UNIQUE(store_id, account_id, call_id, head_key),
				FOREIGN KEY(account_id) REFERENCES billing_accounts
			)`,
			`CREATE INDEX IF NOT EXISTS ` + billingProviderCostHeadIndex + `
				ON billing_provider_cost_heads(store_id, account_id, call_id, head_key, evidence_revision)`,
		}
	case dialect.PG:
		statements = []string{
			`CREATE TABLE IF NOT EXISTS billing_provider_cost_heads (
				id BIGSERIAL PRIMARY KEY,
				store_id TEXT NOT NULL,
				account_id TEXT NOT NULL,
				call_id TEXT NOT NULL,
				head_key TEXT NOT NULL,
				subject_kind TEXT NOT NULL,
				subject_id TEXT NOT NULL,
				subject_json TEXT NOT NULL,
				evidence_revision BIGINT NOT NULL CHECK (evidence_revision > 0),
				input_set_hash TEXT NOT NULL,
				valuation_id TEXT NOT NULL,
				amount_nano BIGINT NOT NULL CHECK (amount_nano >= 0),
				currency TEXT NOT NULL,
				head_version BIGINT NOT NULL CHECK (head_version > 0),
				fence BIGINT NOT NULL CHECK (fence > 0),
				last_operation_key TEXT NOT NULL DEFAULT '',
				last_transaction_id TEXT NOT NULL DEFAULT '',
				created_at_unix BIGINT NOT NULL,
				updated_at_unix BIGINT NOT NULL,
				UNIQUE(store_id, account_id, call_id, head_key),
				FOREIGN KEY(account_id) REFERENCES billing_accounts
			)`,
			`CREATE INDEX IF NOT EXISTS ` + billingProviderCostHeadIndex + `
				ON billing_provider_cost_heads(store_id, account_id, call_id, head_key, evidence_revision)`,
		}
	default:
		return fmt.Errorf("billing provider cost heads schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing provider cost heads DDL: %w", err)
		}
	}
	return nil
}
