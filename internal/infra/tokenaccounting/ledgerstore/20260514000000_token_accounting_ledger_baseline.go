package ledgerstore

import (
	"context"
	"fmt"
	"sync"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	"github.com/uptrace/bun/migrate"
)

const BaselineMigrationName = "20260514000000"

var (
	migrations             = migrate.NewMigrations()
	registerMigrationsOnce sync.Once
)

func registerMigrations() {
	registerMigrationsOnce.Do(func() {
		migrations.MustRegister(baselineUp, func(context.Context, *bun.DB) error { return nil })
	})
}

func runSchemaMigrate(ctx context.Context, db *bun.DB) error {
	registerMigrations()
	migrator := migrate.NewMigrator(db, migrations, migrate.WithTableName("bun_token_accounting_migrations"))
	if err := migrator.Init(ctx); err != nil {
		return fmt.Errorf("migrator init: %w", err)
	}
	if _, err := migrator.Migrate(ctx); err != nil {
		return fmt.Errorf("migrator migrate: %w", err)
	}
	return nil
}

func baselineUp(ctx context.Context, db *bun.DB) error {
	var stmts []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		stmts = sqliteDDL()
	case dialect.PG:
		stmts = postgresDDL()
	default:
		return fmt.Errorf("unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("token accounting baseline: %w", err)
		}
	}
	return nil
}

func sqliteDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS token_accounting_ledger_records (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			request_id TEXT NOT NULL,
			attempt_id TEXT NOT NULL,
			backend TEXT NOT NULL,
			model TEXT NOT NULL,
			plane TEXT NOT NULL,
			input_tokens INTEGER NOT NULL,
			output_tokens INTEGER NOT NULL,
			cache_read_tokens INTEGER NOT NULL,
			cache_write_tokens INTEGER NOT NULL,
			reasoning_tokens INTEGER NOT NULL,
			total_tokens INTEGER NOT NULL,
			metadata_json TEXT NOT NULL,
			created_at_unix INTEGER NOT NULL,
			unavailable_reason TEXT NOT NULL DEFAULT '',
			failure_reason TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_token_accounting_ledger_request ON token_accounting_ledger_records(request_id, id)`,
		`CREATE INDEX IF NOT EXISTS idx_token_accounting_ledger_request_attempt ON token_accounting_ledger_records(request_id, attempt_id, id)`,
	}
}

func postgresDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS token_accounting_ledger_records (
			id BIGSERIAL PRIMARY KEY,
			request_id TEXT NOT NULL,
			attempt_id TEXT NOT NULL,
			backend TEXT NOT NULL,
			model TEXT NOT NULL,
			plane TEXT NOT NULL,
			input_tokens BIGINT NOT NULL,
			output_tokens BIGINT NOT NULL,
			cache_read_tokens BIGINT NOT NULL,
			cache_write_tokens BIGINT NOT NULL,
			reasoning_tokens BIGINT NOT NULL,
			total_tokens BIGINT NOT NULL,
			metadata_json TEXT NOT NULL,
			created_at_unix BIGINT NOT NULL,
			unavailable_reason TEXT NOT NULL DEFAULT '',
			failure_reason TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_token_accounting_ledger_request ON token_accounting_ledger_records(request_id, id)`,
		`CREATE INDEX IF NOT EXISTS idx_token_accounting_ledger_request_attempt ON token_accounting_ledger_records(request_id, attempt_id, id)`,
	}
}
