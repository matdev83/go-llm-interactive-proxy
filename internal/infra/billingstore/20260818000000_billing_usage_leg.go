package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const UsageLegRecordsMigrationName = "20260818000000"

const usageLegCallBLegIndex = "idx_usage_leg_records_call_b_leg"

func registerUsageLegRecordsMigration() {
	migrations.MustRegister(usageLegRecordsSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func usageLegRecordsSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing usage-leg schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = sqliteUsageLegRecordsDDL()
	case dialect.PG:
		statements = postgresUsageLegRecordsDDL()
	default:
		return fmt.Errorf("billing usage-leg schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "duplicate column") || strings.Contains(lower, "already exists") {
				continue
			}
			return fmt.Errorf("billing usage-leg DDL: %w", err)
		}
	}
	return nil
}

func sqliteUsageLegRecordsDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS usage_leg_records (
			usage_leg_key TEXT PRIMARY KEY,
			fingerprint TEXT NOT NULL,
			call_id TEXT NOT NULL,
			a_leg_id TEXT NOT NULL,
			b_leg_id TEXT NOT NULL,
			backend_id TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			model_id TEXT NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT NOT NULL,
			outcome TEXT NOT NULL,
			surfaced TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			sealed_at TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + usageLegCallBLegIndex + ` ON usage_leg_records(call_id, b_leg_id)`,
		`CREATE TRIGGER IF NOT EXISTS billing_usage_leg_immutable_update BEFORE UPDATE ON usage_leg_records BEGIN SELECT RAISE(ABORT, 'usage leg records are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_usage_leg_immutable_delete BEFORE DELETE ON usage_leg_records BEGIN SELECT RAISE(ABORT, 'usage leg records are immutable'); END`,
	}
}

func postgresUsageLegRecordsDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS usage_leg_records (
			usage_leg_key TEXT PRIMARY KEY,
			fingerprint TEXT NOT NULL,
			call_id TEXT NOT NULL,
			a_leg_id TEXT NOT NULL,
			b_leg_id TEXT NOT NULL,
			backend_id TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			model_id TEXT NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT NOT NULL,
			outcome TEXT NOT NULL,
			surfaced TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			sealed_at TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + usageLegCallBLegIndex + ` ON usage_leg_records(call_id, b_leg_id)`,
		`DROP TRIGGER IF EXISTS billing_usage_leg_immutable ON usage_leg_records`,
		`CREATE TRIGGER billing_usage_leg_immutable BEFORE UPDATE OR DELETE ON usage_leg_records FOR EACH ROW EXECUTE FUNCTION billing_reject_immutable()`,
	}
}
