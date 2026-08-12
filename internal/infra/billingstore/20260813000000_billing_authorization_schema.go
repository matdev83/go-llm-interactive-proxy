package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// AuthorizationSchemaMigrationName identifies the forward migration that adds
// authorization identity and account-snapshot evidence to existing billing
// installations. The filename is part of Bun's migration identity convention.
const AuthorizationSchemaMigrationName = "20260813000000"

func registerAuthorizationSchemaMigration() {
	migrations.MustRegister(authorizationSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func authorizationSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing authorization schema: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.PG:
		for _, statement := range postgresAuthorizationSchemaStatements() {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("billing authorization schema postgres: %w", err)
			}
		}
		return nil
	case dialect.SQLite:
		for _, column := range sqliteAuthorizationColumns() {
			if err := sqliteAddBillingColumnIfMissing(ctx, db, column.name, column.definition); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("billing authorization schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}

type billingColumn struct {
	name       string
	definition string
}

func sqliteAuthorizationColumns() []billingColumn {
	return []billingColumn{
		{name: "authorization_id", definition: `ALTER TABLE authorization_holds ADD COLUMN authorization_id TEXT NOT NULL DEFAULT ''`},
		{name: "pricing_ref", definition: `ALTER TABLE authorization_holds ADD COLUMN pricing_ref TEXT NOT NULL DEFAULT ''`},
		{name: "charge_policy_ref", definition: `ALTER TABLE authorization_holds ADD COLUMN charge_policy_ref TEXT NOT NULL DEFAULT ''`},
		{name: "mode", definition: `ALTER TABLE authorization_holds ADD COLUMN mode TEXT NOT NULL DEFAULT ''`},
		{name: "balance_before_nano", definition: `ALTER TABLE authorization_holds ADD COLUMN balance_before_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "balance_after_nano", definition: `ALTER TABLE authorization_holds ADD COLUMN balance_after_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "reserved_before_nano", definition: `ALTER TABLE authorization_holds ADD COLUMN reserved_before_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "reserved_after_nano", definition: `ALTER TABLE authorization_holds ADD COLUMN reserved_after_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "spendable_before_nano", definition: `ALTER TABLE authorization_holds ADD COLUMN spendable_before_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "spendable_after_nano", definition: `ALTER TABLE authorization_holds ADD COLUMN spendable_after_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "credit_floor_nano", definition: `ALTER TABLE authorization_holds ADD COLUMN credit_floor_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "credit_limit_nano", definition: `ALTER TABLE authorization_holds ADD COLUMN credit_limit_nano INTEGER NOT NULL DEFAULT 0`},
		{name: "version_before", definition: `ALTER TABLE authorization_holds ADD COLUMN version_before INTEGER NOT NULL DEFAULT 0`},
		{name: "version_after", definition: `ALTER TABLE authorization_holds ADD COLUMN version_after INTEGER NOT NULL DEFAULT 0`},
		{name: "closed_reason", definition: `ALTER TABLE authorization_holds ADD COLUMN closed_reason TEXT NOT NULL DEFAULT ''`},
	}
}

func sqliteAddBillingColumnIfMissing(ctx context.Context, db *bun.DB, column, alterSQL string) error {
	var count int
	if err := db.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('authorization_holds') WHERE name = ?`, column).Scan(ctx, &count); err != nil {
		return fmt.Errorf("billing authorization schema sqlite probe %s: %w", column, err)
	}
	if count > 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, alterSQL); err != nil {
		// A concurrent migration can add the same column between the probe and
		// ALTER. Treat that narrow race as success; all other errors are fatal.
		if strings.Contains(strings.ToLower(err.Error()), "duplicate column") || strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return nil
		}
		return fmt.Errorf("billing authorization schema sqlite add %s: %w", column, err)
	}
	return nil
}

func postgresAuthorizationSchemaStatements() []string {
	return []string{
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS authorization_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS pricing_ref TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS charge_policy_ref TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS balance_before_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS balance_after_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS reserved_before_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS reserved_after_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS spendable_before_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS spendable_after_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS credit_floor_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS credit_limit_nano BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS version_before BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS version_after BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE authorization_holds ADD COLUMN IF NOT EXISTS closed_reason TEXT NOT NULL DEFAULT ''`,
	}
}
