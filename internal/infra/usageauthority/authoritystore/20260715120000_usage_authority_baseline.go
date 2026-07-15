package authoritystore

import (
	"context"
	"fmt"
	"sync"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	"github.com/uptrace/bun/migrate"
)

// BaselineMigrationName is the name bun/migrate records for this adapter's
// baseline schema migration.
const BaselineMigrationName = "20260715120000"

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
	migrator := migrate.NewMigrator(db, migrations, migrate.WithTableName("bun_usage_authority_migrations"))
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
	case dialect.SQLite, dialect.PG:
		stmts = baselineDDL()
	default:
		return fmt.Errorf("unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("usage authority baseline: %w", err)
		}
	}
	return nil
}

func baselineDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS usage_authority_state (
			store_id TEXT NOT NULL PRIMARY KEY,
			readiness_json TEXT NOT NULL,
			next_decision_seq INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS usage_authority_limit_rows (
			store_id TEXT NOT NULL,
			row_key TEXT NOT NULL,
			row_json TEXT NOT NULL,
			PRIMARY KEY (store_id, row_key)
		)`,
		`CREATE TABLE IF NOT EXISTS usage_authority_decisions (
			store_id TEXT NOT NULL,
			decision_seq INTEGER NOT NULL,
			source_key TEXT NOT NULL,
			row_json TEXT NOT NULL,
			PRIMARY KEY (store_id, decision_seq),
			UNIQUE (store_id, source_key)
		)`,
		`CREATE TABLE IF NOT EXISTS usage_authority_decision_filters (
			store_id TEXT NOT NULL,
			decision_seq INTEGER NOT NULL,
			field_name TEXT NOT NULL,
			field_value TEXT NOT NULL,
			PRIMARY KEY (store_id, decision_seq, field_name)
		)`,
		`CREATE INDEX IF NOT EXISTS usage_authority_decision_filters_lookup
			ON usage_authority_decision_filters(store_id, field_name, field_value, decision_seq)`,
		`CREATE TABLE IF NOT EXISTS usage_authority_limit_filters (
			store_id TEXT NOT NULL,
			row_key TEXT NOT NULL,
			field_name TEXT NOT NULL,
			field_value TEXT NOT NULL,
			PRIMARY KEY (store_id, row_key, field_name)
		)`,
		`CREATE INDEX IF NOT EXISTS usage_authority_limit_filters_lookup
			ON usage_authority_limit_filters(store_id, field_name, field_value, row_key)`,
		`CREATE TABLE IF NOT EXISTS usage_authority_reservations (
			store_id TEXT NOT NULL,
			reservation_key TEXT NOT NULL,
			source_key TEXT NOT NULL,
			record_json TEXT NOT NULL,
			PRIMARY KEY (store_id, reservation_key),
			UNIQUE (store_id, source_key)
		)`,
		`CREATE TABLE IF NOT EXISTS usage_authority_unreserved_usage_facts (
			store_id TEXT NOT NULL,
			fact_key TEXT NOT NULL,
			record_json TEXT NOT NULL,
			PRIMARY KEY (store_id, fact_key)
		)`,
	}
}
