package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingOperatorCursorKeyMigrationName adds the server-owned secret that
// authenticates durable operator-reader cursors. The key is generated once per
// store identity, persisted with the owned billing data, and used only as an
// HMAC key: it is never embedded in a cursor, response, log or error.
const BillingOperatorCursorKeyMigrationName = "20260930000002"

// billingOperatorCursorKeyTable holds one high-entropy key per durable store
// identity. It is deliberately separate from cursor payloads and carries no
// customer evidence.
const billingOperatorCursorKeyTable = "billing_operator_cursor_keys"

func registerBillingOperatorCursorKeyMigration() {
	migrations.MustRegister(billingOperatorCursorKeyUp, func(context.Context, *bun.DB) error { return nil })
}

func billingOperatorCursorKeyUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing operator cursor key: nil database")
	}
	if ctx == nil {
		return fmt.Errorf("billing operator cursor key: nil context")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = sqliteBillingOperatorCursorKeyDDL()
	case dialect.PG:
		statements = postgresBillingOperatorCursorKeyDDL()
	default:
		return fmt.Errorf("billing operator cursor key: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "already exists") || strings.Contains(lower, "duplicate") {
				continue
			}
			return fmt.Errorf("billing operator cursor key DDL: %w", err)
		}
	}
	return nil
}

func sqliteBillingOperatorCursorKeyDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS ` + billingOperatorCursorKeyTable + ` (
			store_id TEXT NOT NULL PRIMARY KEY,
			key_material BLOB NOT NULL,
			created_at_unix INTEGER NOT NULL
		)`,
	}
}

func postgresBillingOperatorCursorKeyDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS ` + billingOperatorCursorKeyTable + ` (
			store_id TEXT PRIMARY KEY,
			key_material BYTEA NOT NULL,
			created_at_unix BIGINT NOT NULL
		)`,
	}
}
