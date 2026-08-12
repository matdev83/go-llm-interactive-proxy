package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const SessionIDMigrationName = "20260817000000"

const sessionAccountIndex = "idx_billing_tur_account_session"

func registerSessionIDMigration() {
	migrations.MustRegister(sessionIDSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func sessionIDSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing session schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = []string{
			`ALTER TABLE turn_usage_records ADD COLUMN session_id TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX IF NOT EXISTS ` + sessionAccountIndex + ` ON turn_usage_records(account_id, session_id, tur_key)`,
		}
	case dialect.PG:
		statements = []string{
			`ALTER TABLE turn_usage_records ADD COLUMN IF NOT EXISTS session_id TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX IF NOT EXISTS ` + sessionAccountIndex + ` ON turn_usage_records(account_id, session_id, tur_key)`,
		}
	default:
		return fmt.Errorf("billing session schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "duplicate column") || strings.Contains(lower, "already exists") {
				continue
			}
			return fmt.Errorf("billing session DDL: %w", err)
		}
	}
	return nil
}
