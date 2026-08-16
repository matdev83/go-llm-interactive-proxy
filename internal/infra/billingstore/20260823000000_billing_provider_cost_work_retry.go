package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const ProviderCostWorkRetryMigrationName = "20260823000000"

func registerProviderCostWorkRetryMigration() {
	migrations.MustRegister(providerCostWorkRetrySchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func providerCostWorkRetrySchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing provider-cost retry schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = []string{
			`ALTER TABLE provider_cost_work ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE provider_cost_work ADD COLUMN next_attempt_at TEXT NOT NULL DEFAULT ''`,
			`UPDATE provider_cost_work SET next_attempt_at = updated_at WHERE next_attempt_at = ''`,
			`ALTER TABLE provider_cost_work ADD COLUMN last_error TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX IF NOT EXISTS ` + providerCostWorkPendingIndex + ` ON provider_cost_work(status, next_attempt_at, updated_at, usage_leg_key)`,
		}
	case dialect.PG:
		statements = []string{
			`ALTER TABLE provider_cost_work ADD COLUMN IF NOT EXISTS attempt_count INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE provider_cost_work ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP`,
			`ALTER TABLE provider_cost_work ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX IF NOT EXISTS ` + providerCostWorkPendingIndex + ` ON provider_cost_work(status, next_attempt_at, updated_at, usage_leg_key)`,
		}
	default:
		return fmt.Errorf("billing provider-cost retry schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "duplicate column") || strings.Contains(lower, "already exists") {
				continue
			}
			return fmt.Errorf("billing provider-cost retry DDL: %w", err)
		}
	}
	return nil
}
