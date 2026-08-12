package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const Phase4MigrationName = "20260814000000"

func registerPhase4Migration() {
	migrations.MustRegister(phase4SchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func phase4SchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing phase4 schema: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite:
		for _, column := range sqlitePhase4Columns() {
			if err := sqliteAddColumnIfMissing(ctx, db, column.name, column.definition); err != nil {
				return err
			}
		}
		for _, stmt := range sqlitePhase4Tables() {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("billing phase4 sqlite DDL: %w", err)
			}
		}
	case dialect.PG:
		for _, stmt := range postgresPhase4Statements() {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("billing phase4 postgres DDL: %w", err)
			}
		}
	default:
		return fmt.Errorf("billing phase4 schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	return nil
}

func sqlitePhase4Columns() []billingColumn {
	return []billingColumn{
		{name: "released_amount_nano", definition: `ALTER TABLE authorization_holds ADD COLUMN released_amount_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "closed_source_key", definition: `ALTER TABLE authorization_holds ADD COLUMN closed_source_key TEXT NOT NULL DEFAULT ''`},
		{name: "closed_fingerprint", definition: `ALTER TABLE authorization_holds ADD COLUMN closed_fingerprint TEXT NOT NULL DEFAULT ''`},
		{name: "closed_amount_nano", definition: `ALTER TABLE authorization_holds ADD COLUMN closed_amount_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "operation_kind", definition: `ALTER TABLE journal_transactions ADD COLUMN operation_kind TEXT NOT NULL DEFAULT ''`},
		{name: "balance_before_nano", definition: `ALTER TABLE journal_transactions ADD COLUMN balance_before_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "balance_after_nano", definition: `ALTER TABLE journal_transactions ADD COLUMN balance_after_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "reserved_before_nano", definition: `ALTER TABLE journal_transactions ADD COLUMN reserved_before_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "reserved_after_nano", definition: `ALTER TABLE journal_transactions ADD COLUMN reserved_after_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "spendable_before_nano", definition: `ALTER TABLE journal_transactions ADD COLUMN spendable_before_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "spendable_after_nano", definition: `ALTER TABLE journal_transactions ADD COLUMN spendable_after_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "credit_floor_nano", definition: `ALTER TABLE journal_transactions ADD COLUMN credit_floor_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "credit_limit_nano", definition: `ALTER TABLE journal_transactions ADD COLUMN credit_limit_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "mode", definition: `ALTER TABLE journal_transactions ADD COLUMN mode TEXT NOT NULL DEFAULT ''`},
		{name: "snapshot_version_before", definition: `ALTER TABLE journal_transactions ADD COLUMN snapshot_version_before INTEGER NOT NULL DEFAULT 0`},
		{name: "snapshot_version_after", definition: `ALTER TABLE journal_transactions ADD COLUMN snapshot_version_after INTEGER NOT NULL DEFAULT 0`},
	}
}

func sqlitePhase4Tables() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS billing_operation_snapshots (
			operation_key TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			operation_kind TEXT NOT NULL,
			source_key TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			integrity_fingerprint TEXT NOT NULL DEFAULT '',
			currency TEXT NOT NULL,
			mode TEXT NOT NULL,
			balance_before_nano INTEGER NOT NULL,
			balance_after_nano INTEGER NOT NULL,
			reserved_before_nano INTEGER NOT NULL,
			reserved_after_nano INTEGER NOT NULL,
			spendable_before_nano INTEGER NOT NULL,
			spendable_after_nano INTEGER NOT NULL,
			credit_floor_nano INTEGER NOT NULL,
			credit_limit_nano INTEGER NOT NULL,
			version_before INTEGER NOT NULL,
			version_after INTEGER NOT NULL,
			account_sequence_start INTEGER NOT NULL DEFAULT 0,
			account_sequence_end INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			UNIQUE(account_id, operation_kind, source_key),
			FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_operation_snapshots_account ON billing_operation_snapshots(account_id, version_after)`,
	}
}

func postgresPhase4Statements() []string {
	return []string{
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS released_amount_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS closed_source_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS closed_fingerprint TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS closed_amount_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE journal_transactions ADD COLUMN IF NOT EXISTS operation_kind TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE journal_transactions ADD COLUMN IF NOT EXISTS balance_before_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE journal_transactions ADD COLUMN IF NOT EXISTS balance_after_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE journal_transactions ADD COLUMN IF NOT EXISTS reserved_before_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE journal_transactions ADD COLUMN IF NOT EXISTS reserved_after_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE journal_transactions ADD COLUMN IF NOT EXISTS spendable_before_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE journal_transactions ADD COLUMN IF NOT EXISTS spendable_after_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE journal_transactions ADD COLUMN IF NOT EXISTS credit_floor_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE journal_transactions ADD COLUMN IF NOT EXISTS credit_limit_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE journal_transactions ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE journal_transactions ADD COLUMN IF NOT EXISTS snapshot_version_before BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE journal_transactions ADD COLUMN IF NOT EXISTS snapshot_version_after BIGINT NOT NULL DEFAULT 0`,
		`CREATE TABLE IF NOT EXISTS billing_operation_snapshots (
			operation_key TEXT PRIMARY KEY, account_id TEXT NOT NULL, operation_kind TEXT NOT NULL,
			source_key TEXT NOT NULL, fingerprint TEXT NOT NULL, integrity_fingerprint TEXT NOT NULL DEFAULT '',
			currency TEXT NOT NULL, mode TEXT NOT NULL,
			balance_before_nano BIGINT NOT NULL, balance_after_nano BIGINT NOT NULL,
			reserved_before_nano BIGINT NOT NULL, reserved_after_nano BIGINT NOT NULL,
			spendable_before_nano BIGINT NOT NULL, spendable_after_nano BIGINT NOT NULL,
			credit_floor_nano BIGINT NOT NULL, credit_limit_nano BIGINT NOT NULL,
			version_before BIGINT NOT NULL, version_after BIGINT NOT NULL,
			account_sequence_start BIGINT NOT NULL DEFAULT 0, account_sequence_end BIGINT NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL, UNIQUE(account_id, operation_kind, source_key),
			FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_operation_snapshots_account ON billing_operation_snapshots(account_id, version_after)`,
	}
}

func sqliteAddColumnIfMissing(ctx context.Context, db *bun.DB, column, alterSQL string) error {
	var count int
	if strings.Contains(alterSQL, "authorization_holds") {
		if err := db.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('authorization_holds') WHERE name = ?`, column).Scan(ctx, &count); err != nil {
			return fmt.Errorf("billing phase4 sqlite probe %s: %w", column, err)
		}
	} else {
		if err := db.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('journal_transactions') WHERE name = ?`, column).Scan(ctx, &count); err != nil {
			return fmt.Errorf("billing phase4 sqlite probe %s: %w", column, err)
		}
	}
	if count > 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, alterSQL); err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "duplicate column") || strings.Contains(lower, "already exists") {
			return nil
		}
		return fmt.Errorf("billing phase4 sqlite add %s: %w", column, err)
	}
	return nil
}
