package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingStatementImportMigrationName identifies the additive durable
// normalized statement import ledger for Task 13.1B: one immutable statement
// revision envelope plus its independent immutable line claims.
const BillingStatementImportMigrationName = "20260928000000"

const (
	billingStatementRevisionScopeIndex  = "idx_billing_statement_revisions_scope"
	billingStatementRevisionTenantIndex = "idx_billing_statement_revisions_tenant"
	billingStatementLineStatementIndex  = "idx_billing_statement_lines_statement"
	billingStatementLineScopeIndex      = "idx_billing_statement_lines_scope"
)

// registerBillingStatementImportMigration adds the statement import ledger
// schema. Existing billing migrations and tables are untouched.
func registerBillingStatementImportMigration() {
	migrations.MustRegister(billingStatementImportSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingStatementImportSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing statement import schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = sqliteBillingStatementImportDDL()
	case dialect.PG:
		statements = postgresBillingStatementImportDDL()
	default:
		return fmt.Errorf("billing statement import schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing statement import DDL: %w", err)
		}
	}
	return nil
}

func sqliteBillingStatementImportDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS billing_statement_revisions (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			statement_key TEXT NOT NULL,
			provider_account_key TEXT NOT NULL,
			statement_id TEXT NOT NULL,
			period_id TEXT NOT NULL,
			revision INTEGER NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT '',
			principal_id TEXT NOT NULL DEFAULT '',
			schema_version INTEGER NOT NULL DEFAULT 1,
			fingerprint TEXT NOT NULL,
			scope_json TEXT NOT NULL,
			envelope_json TEXT NOT NULL,
			received_at_unix INTEGER NOT NULL,
			UNIQUE(store_id, statement_key)
		)`,
		`CREATE INDEX IF NOT EXISTS ` + billingStatementRevisionScopeIndex + `
			ON billing_statement_revisions(store_id, provider_account_key, statement_id, period_id, revision, statement_key)`,
		`CREATE INDEX IF NOT EXISTS ` + billingStatementRevisionTenantIndex + `
			ON billing_statement_revisions(store_id, tenant_id, provider_account_key, statement_id, period_id, statement_key)`,
		`CREATE TABLE IF NOT EXISTS billing_statement_lines (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			line_key TEXT NOT NULL,
			statement_key TEXT NOT NULL,
			envelope_revision INTEGER NOT NULL,
			provider_account_key TEXT NOT NULL,
			statement_id TEXT NOT NULL,
			period_id TEXT NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT '',
			line_id TEXT NOT NULL,
			line_revision INTEGER NOT NULL,
			outcome TEXT NOT NULL DEFAULT '',
			charge_item_id TEXT NOT NULL DEFAULT '',
			observation_id TEXT NOT NULL DEFAULT '',
			observation_revision INTEGER NOT NULL DEFAULT 0,
			fingerprint TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			UNIQUE(store_id, line_key),
			FOREIGN KEY(store_id, statement_key) REFERENCES billing_statement_revisions(store_id, statement_key)
		)`,
		`CREATE INDEX IF NOT EXISTS ` + billingStatementLineStatementIndex + `
			ON billing_statement_lines(store_id, statement_key, line_id, line_revision, line_key)`,
		`CREATE INDEX IF NOT EXISTS ` + billingStatementLineScopeIndex + `
			ON billing_statement_lines(store_id, provider_account_key, statement_id, period_id, outcome, line_key)`,
		`CREATE TRIGGER IF NOT EXISTS billing_statement_revisions_immutable_update BEFORE UPDATE ON billing_statement_revisions
			BEGIN SELECT RAISE(ABORT, 'billing statement revision is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_statement_revisions_immutable_delete BEFORE DELETE ON billing_statement_revisions
			BEGIN SELECT RAISE(ABORT, 'billing statement revision is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_statement_lines_immutable_update BEFORE UPDATE ON billing_statement_lines
			BEGIN SELECT RAISE(ABORT, 'billing statement line is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_statement_lines_immutable_delete BEFORE DELETE ON billing_statement_lines
			BEGIN SELECT RAISE(ABORT, 'billing statement line is immutable'); END`,
	}
}

func postgresBillingStatementImportDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS billing_statement_revisions (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL,
			statement_key TEXT NOT NULL,
			provider_account_key TEXT NOT NULL,
			statement_id TEXT NOT NULL,
			period_id TEXT NOT NULL,
			revision BIGINT NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT '',
			principal_id TEXT NOT NULL DEFAULT '',
			schema_version INTEGER NOT NULL DEFAULT 1,
			fingerprint TEXT NOT NULL,
			scope_json TEXT NOT NULL,
			envelope_json TEXT NOT NULL,
			received_at_unix BIGINT NOT NULL,
			UNIQUE(store_id, statement_key)
		)`,
		`CREATE INDEX IF NOT EXISTS ` + billingStatementRevisionScopeIndex + `
			ON billing_statement_revisions(store_id, provider_account_key, statement_id, period_id, revision, statement_key)`,
		`CREATE INDEX IF NOT EXISTS ` + billingStatementRevisionTenantIndex + `
			ON billing_statement_revisions(store_id, tenant_id, provider_account_key, statement_id, period_id, statement_key)`,
		`CREATE TABLE IF NOT EXISTS billing_statement_lines (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL,
			line_key TEXT NOT NULL,
			statement_key TEXT NOT NULL,
			envelope_revision BIGINT NOT NULL,
			provider_account_key TEXT NOT NULL,
			statement_id TEXT NOT NULL,
			period_id TEXT NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT '',
			line_id TEXT NOT NULL,
			line_revision BIGINT NOT NULL,
			outcome TEXT NOT NULL DEFAULT '',
			charge_item_id TEXT NOT NULL DEFAULT '',
			observation_id TEXT NOT NULL DEFAULT '',
			observation_revision BIGINT NOT NULL DEFAULT 0,
			fingerprint TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			UNIQUE(store_id, line_key),
			FOREIGN KEY(store_id, statement_key) REFERENCES billing_statement_revisions(store_id, statement_key)
		)`,
		`CREATE INDEX IF NOT EXISTS ` + billingStatementLineStatementIndex + `
			ON billing_statement_lines(store_id, statement_key, line_id, line_revision, line_key)`,
		`CREATE INDEX IF NOT EXISTS ` + billingStatementLineScopeIndex + `
			ON billing_statement_lines(store_id, provider_account_key, statement_id, period_id, outcome, line_key)`,
		`CREATE OR REPLACE FUNCTION billing_reject_statement_import_mutation() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'billing statement import record is immutable'; END; $$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS billing_statement_revisions_immutable ON billing_statement_revisions`,
		`CREATE TRIGGER billing_statement_revisions_immutable BEFORE UPDATE OR DELETE ON billing_statement_revisions FOR EACH ROW EXECUTE FUNCTION billing_reject_statement_import_mutation()`,
		`DROP TRIGGER IF EXISTS billing_statement_lines_immutable ON billing_statement_lines`,
		`CREATE TRIGGER billing_statement_lines_immutable BEFORE UPDATE OR DELETE ON billing_statement_lines FOR EACH ROW EXECUTE FUNCTION billing_reject_statement_import_mutation()`,
	}
}
