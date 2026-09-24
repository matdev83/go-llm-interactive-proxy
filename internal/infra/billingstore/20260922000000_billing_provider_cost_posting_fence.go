package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingProviderCostPostingFenceMigrationName identifies the durable
// cross-path provider-cost posting identity. Legacy LUR work and V2 economic
// revisions both claim this row before they can write provider COGS.
const BillingProviderCostPostingFenceMigrationName = "20260922000000"

const billingProviderCostPostingFenceIndex = "idx_billing_provider_cost_posting_fences_scope"

func registerBillingProviderCostPostingFenceMigration() {
	migrations.MustRegister(billingProviderCostPostingFenceSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingProviderCostPostingFenceSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing provider cost posting fence schema: nil database")
	}
	if ctx == nil {
		return fmt.Errorf("billing provider cost posting fence schema: nil context")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = []string{
			`CREATE TABLE IF NOT EXISTS billing_provider_cost_posting_fences (
				id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
				store_id TEXT NOT NULL,
				account_id TEXT NOT NULL,
				call_id TEXT NOT NULL,
				lineage_key TEXT NOT NULL,
				authority TEXT NOT NULL CHECK (authority IN ('legacy', 'revision')),
				head_key TEXT NOT NULL,
				evidence_revision INTEGER NOT NULL CHECK (evidence_revision > 0),
				input_set_hash TEXT NOT NULL,
				fingerprint TEXT NOT NULL,
				amount_nano INTEGER NOT NULL CHECK (amount_nano >= 0),
				currency TEXT NOT NULL,
				fence INTEGER NOT NULL CHECK (fence > 0),
				last_operation_key TEXT NOT NULL,
				last_transaction_id TEXT NOT NULL DEFAULT '',
				created_at_unix INTEGER NOT NULL,
				updated_at_unix INTEGER NOT NULL,
				UNIQUE(store_id, account_id, call_id, lineage_key),
				FOREIGN KEY(account_id) REFERENCES billing_accounts
			)`,
			`CREATE INDEX IF NOT EXISTS ` + billingProviderCostPostingFenceIndex + `
				ON billing_provider_cost_posting_fences(store_id, account_id, call_id, lineage_key, evidence_revision)`,
		}
	case dialect.PG:
		statements = []string{
			`CREATE TABLE IF NOT EXISTS billing_provider_cost_posting_fences (
				id BIGSERIAL PRIMARY KEY,
				store_id TEXT NOT NULL,
				account_id TEXT NOT NULL,
				call_id TEXT NOT NULL,
				lineage_key TEXT NOT NULL,
				authority TEXT NOT NULL CHECK (authority IN ('legacy', 'revision')),
				head_key TEXT NOT NULL,
				evidence_revision BIGINT NOT NULL CHECK (evidence_revision > 0),
				input_set_hash TEXT NOT NULL,
				fingerprint TEXT NOT NULL,
				amount_nano BIGINT NOT NULL CHECK (amount_nano >= 0),
				currency TEXT NOT NULL,
				fence BIGINT NOT NULL CHECK (fence > 0),
				last_operation_key TEXT NOT NULL,
				last_transaction_id TEXT NOT NULL DEFAULT '',
				created_at_unix BIGINT NOT NULL,
				updated_at_unix BIGINT NOT NULL,
				UNIQUE(store_id, account_id, call_id, lineage_key),
				FOREIGN KEY(account_id) REFERENCES billing_accounts
			)`,
			`CREATE INDEX IF NOT EXISTS ` + billingProviderCostPostingFenceIndex + `
				ON billing_provider_cost_posting_fences(store_id, account_id, call_id, lineage_key, evidence_revision)`,
		}
	default:
		return fmt.Errorf("billing provider cost posting fence schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing provider cost posting fence DDL: %w", err)
		}
	}
	return nil
}
