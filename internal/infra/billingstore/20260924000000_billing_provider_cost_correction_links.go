package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingProviderCostCorrectionLinksMigrationName identifies the additive
// provider-cost identity repair. Heads and posting fences retain both the
// original and latest journal transaction IDs so correction links can be
// reconstructed after restart or legacy adoption.
const BillingProviderCostCorrectionLinksMigrationName = "20260924000000"

func registerBillingProviderCostCorrectionLinksMigration() {
	migrations.MustRegister(billingProviderCostCorrectionLinksSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingProviderCostCorrectionLinksSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing provider cost correction links schema: nil database")
	}
	if ctx == nil {
		return fmt.Errorf("billing provider cost correction links schema: nil context")
	}
	statements := []string{
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN original_transaction_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE billing_provider_cost_posting_fences ADD COLUMN original_transaction_id TEXT NOT NULL DEFAULT ''`,
		`UPDATE billing_provider_cost_heads SET original_transaction_id = last_transaction_id WHERE original_transaction_id = '' AND last_transaction_id <> ''`,
		`UPDATE billing_provider_cost_posting_fences SET original_transaction_id = last_transaction_id WHERE original_transaction_id = '' AND last_transaction_id <> ''`,
	}
	if db.Dialect().Name() == dialect.PG {
		statements = []string{
			`ALTER TABLE billing_provider_cost_heads ADD COLUMN IF NOT EXISTS original_transaction_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE billing_provider_cost_posting_fences ADD COLUMN IF NOT EXISTS original_transaction_id TEXT NOT NULL DEFAULT ''`,
			`UPDATE billing_provider_cost_heads SET original_transaction_id = last_transaction_id WHERE original_transaction_id = '' AND last_transaction_id <> ''`,
			`UPDATE billing_provider_cost_posting_fences SET original_transaction_id = last_transaction_id WHERE original_transaction_id = '' AND last_transaction_id <> ''`,
		}
	} else if db.Dialect().Name() != dialect.SQLite {
		return fmt.Errorf("billing provider cost correction links schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			// SQLite has no ADD COLUMN IF NOT EXISTS. A partially applied migration
			// must remain safely retryable after a process interruption.
			lower := strings.ToLower(err.Error())
			if db.Dialect().Name() == dialect.SQLite && (strings.Contains(lower, "duplicate column") || strings.Contains(lower, "already exists")) {
				continue
			}
			return fmt.Errorf("billing provider cost correction links DDL: %w", err)
		}
	}
	return nil
}
