package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const ProviderCostWorkMigrationName = "20260822000000"

const (
	providerCostWorkStatusIndex  = "idx_provider_cost_work_status"
	providerCostWorkPendingIndex = "idx_provider_cost_work_pending"
)

func registerProviderCostWorkMigration() {
	migrations.MustRegister(providerCostWorkSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func providerCostWorkSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing provider-cost work schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = sqliteProviderCostWorkDDL()
	case dialect.PG:
		statements = postgresProviderCostWorkDDL()
	default:
		return fmt.Errorf("billing provider-cost work schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "duplicate column") || strings.Contains(lower, "already exists") {
				continue
			}
			return fmt.Errorf("billing provider-cost work DDL: %w", err)
		}
	}
	return nil
}

func sqliteProviderCostWorkDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS provider_cost_work (
			usage_leg_key TEXT PRIMARY KEY,
			call_id TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			attempt_count INTEGER NOT NULL DEFAULT 0,
			next_attempt_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_error TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS ` + providerCostWorkStatusIndex + ` ON provider_cost_work(status, updated_at, usage_leg_key)`,
		`CREATE INDEX IF NOT EXISTS ` + providerCostWorkPendingIndex + ` ON provider_cost_work(status, next_attempt_at, updated_at, usage_leg_key)`,
		// sealed_at is TEXT on usage_leg_records; keep TEXT timestamps consistent
		// on SQLite by writing CURRENT_TIMESTAMP rather than mixing formats.
		`INSERT OR IGNORE INTO provider_cost_work(usage_leg_key, call_id, status, attempt_count, next_attempt_at, last_error, updated_at)
			SELECT usage_leg_key, call_id, 'pending', 0, CURRENT_TIMESTAMP, '', CURRENT_TIMESTAMP FROM usage_leg_records`,
	}
}

func postgresProviderCostWorkDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS provider_cost_work (
			usage_leg_key TEXT PRIMARY KEY,
			call_id TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			attempt_count INTEGER NOT NULL DEFAULT 0,
			next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_error TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS ` + providerCostWorkStatusIndex + ` ON provider_cost_work(status, updated_at, usage_leg_key)`,
		`CREATE INDEX IF NOT EXISTS ` + providerCostWorkPendingIndex + ` ON provider_cost_work(status, next_attempt_at, updated_at, usage_leg_key)`,
		// usage_leg_records.sealed_at is TEXT; never INSERT it into TIMESTAMPTZ
		// columns (SQLSTATE 42804). Fresh and upgrade backfill use timestamptz now.
		`INSERT INTO provider_cost_work(usage_leg_key, call_id, status, attempt_count, next_attempt_at, last_error, updated_at)
			SELECT usage_leg_key, call_id, 'pending', 0, CURRENT_TIMESTAMP, '', CURRENT_TIMESTAMP FROM usage_leg_records
			ON CONFLICT (usage_leg_key) DO NOTHING`,
	}
}
