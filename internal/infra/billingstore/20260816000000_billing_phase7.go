package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const Phase7MigrationName = "20260816000000"

const journalReversalUniqueIndex = "idx_billing_journal_reversal_unique"

func registerPhase7Migration() {
	migrations.MustRegister(phase7SchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func phase7SchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing phase7 schema: nil database")
	}
	var statement string
	switch db.Dialect().Name() {
	case dialect.SQLite, dialect.PG:
		// One non-empty reversal_of per (account, book). Empty strings are
		// excluded so ordinary journals remain unconstrained.
		statement = `CREATE UNIQUE INDEX IF NOT EXISTS ` + journalReversalUniqueIndex + ` ON journal_transactions(account_id, book, reversal_of) WHERE reversal_of <> ''`
	default:
		return fmt.Errorf("billing phase7 schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	if _, err := db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("billing phase7 DDL: %w", err)
	}
	return nil
}
