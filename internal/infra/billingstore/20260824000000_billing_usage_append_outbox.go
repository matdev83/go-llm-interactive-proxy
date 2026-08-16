package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const UsageAppendOutboxMigrationName = "20260824000000"

const usageAppendOutboxPendingIndex = "idx_usage_append_outbox_pending"

func registerUsageAppendOutboxMigration() {
	migrations.MustRegister(usageAppendOutboxSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func usageAppendOutboxSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing usage append outbox schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = sqliteUsageAppendOutboxDDL()
	case dialect.PG:
		statements = postgresUsageAppendOutboxDDL()
	default:
		return fmt.Errorf("billing usage append outbox schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "already exists") {
				continue
			}
			return fmt.Errorf("billing usage append outbox DDL: %w", err)
		}
	}
	return nil
}

func sqliteUsageAppendOutboxDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS usage_append_outbox (
			append_key TEXT PRIMARY KEY,
			kind TEXT NOT NULL CHECK (kind IN ('call','leg')),
			call_id TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processed','failed')),
			attempt_count INTEGER NOT NULL DEFAULT 0,
			next_attempt_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS ` + usageAppendOutboxPendingIndex + ` ON usage_append_outbox(status, next_attempt_at, updated_at, append_key)`,
	}
}

func postgresUsageAppendOutboxDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS usage_append_outbox (
			append_key TEXT PRIMARY KEY,
			kind TEXT NOT NULL CHECK (kind IN ('call','leg')),
			call_id TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processed','failed')),
			attempt_count INTEGER NOT NULL DEFAULT 0,
			next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS ` + usageAppendOutboxPendingIndex + ` ON usage_append_outbox(status, next_attempt_at, updated_at, append_key)`,
	}
}
