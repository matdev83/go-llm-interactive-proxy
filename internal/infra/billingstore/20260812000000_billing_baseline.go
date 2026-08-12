package billingstore

import (
	"context"
	"fmt"
	"sync"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	"github.com/uptrace/bun/migrate"
)

const BaselineMigrationName = "20260812000000"

var (
	migrations             = migrate.NewMigrations()
	registerMigrationsOnce sync.Once
)

func registerMigrations() {
	registerMigrationsOnce.Do(func() {
		migrations.MustRegister(baselineUp, func(context.Context, *bun.DB) error { return nil })
		registerAuthorizationSchemaMigration()
		registerPhase4Migration()
		registerPhase6Migration()
		registerPhase7Migration()
		registerSessionIDMigration()
	})
}

func runSchemaMigrate(ctx context.Context, database *bun.DB) error {
	registerMigrations()
	migrator := migrate.NewMigrator(database, migrations, migrate.WithTableName("bun_billing_migrations"))
	if err := migrator.Init(ctx); err != nil {
		return fmt.Errorf("billingstore: migrator init: %w", err)
	}
	if _, err := migrator.Migrate(ctx); err != nil {
		return fmt.Errorf("billingstore: migrator migrate: %w", err)
	}
	return nil
}

func baselineUp(ctx context.Context, database *bun.DB) error {
	var statements []string
	switch database.Dialect().Name() {
	case dialect.SQLite:
		statements = sqliteDDL()
	case dialect.PG:
		statements = postgresDDL()
	default:
		return fmt.Errorf("billingstore: unsupported bun dialect %s", database.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billingstore: baseline DDL: %w", err)
		}
	}
	return nil
}

func sqliteDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS billing_accounts (
			account_id TEXT PRIMARY KEY,
			currency TEXT NOT NULL,
			mode TEXT NOT NULL CHECK (mode IN ('prepaid','postpaid')),
			credit_limit_nano INTEGER NOT NULL CHECK (credit_limit_nano >= 0),
			balance_nano INTEGER NOT NULL,
			reserved_nano INTEGER NOT NULL CHECK (reserved_nano >= 0),
			version INTEGER NOT NULL CHECK (version >= 0),
			state TEXT NOT NULL CHECK (state IN ('ready','reconcile_required')),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			CHECK (mode <> 'prepaid' OR credit_limit_nano = 0)
		)`,
		`CREATE TABLE IF NOT EXISTS billing_account_policy_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			account_id TEXT NOT NULL,
			event_key TEXT NOT NULL,
			mode TEXT NOT NULL,
			currency TEXT NOT NULL,
			credit_limit_nano INTEGER NOT NULL CHECK (credit_limit_nano >= 0),
			effective_at TEXT NOT NULL,
			source_key TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE(account_id, event_key),
			UNIQUE(account_id, source_key),
			FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
		)`,
		`CREATE TABLE IF NOT EXISTS authorization_holds (				hold_key TEXT PRIMARY KEY,
			authorization_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			tur_key TEXT NOT NULL,
			currency TEXT NOT NULL,
			amount_nano INTEGER NOT NULL CHECK (amount_nano >= 0),
			status TEXT NOT NULL CHECK (status IN ('open','closed')),
			source_key TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			pricing_ref TEXT NOT NULL DEFAULT '',
			charge_policy_ref TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL DEFAULT '',
			balance_before_nano INTEGER NOT NULL DEFAULT 0,
			balance_after_nano INTEGER NOT NULL DEFAULT 0,
			reserved_before_nano INTEGER NOT NULL DEFAULT 0,
			reserved_after_nano INTEGER NOT NULL DEFAULT 0,
			spendable_before_nano INTEGER NOT NULL DEFAULT 0,
			spendable_after_nano INTEGER NOT NULL DEFAULT 0,
			credit_floor_nano INTEGER NOT NULL DEFAULT 0,
			credit_limit_nano INTEGER NOT NULL DEFAULT 0,
			version_before INTEGER NOT NULL DEFAULT 0,
			version_after INTEGER NOT NULL DEFAULT 0,
			closed_reason TEXT NOT NULL DEFAULT '',
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL,
			closed_at TEXT,
			UNIQUE(account_id, tur_key),
			UNIQUE(account_id, source_key),
			FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
		)`,
		`CREATE TABLE IF NOT EXISTS turn_usage_records (
			tur_key TEXT PRIMARY KEY,
			fingerprint TEXT NOT NULL,
			schema_version INTEGER NOT NULL CHECK (schema_version > 0),
			account_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			a_leg_id TEXT NOT NULL,
			authorization_id TEXT NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT NOT NULL,
			outcome TEXT NOT NULL,
			customer_pricing_ref TEXT NOT NULL,
			charge_policy_ref TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			sealed_at TEXT NOT NULL,
			UNIQUE(account_id, turn_id),
			FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
		)`,
		`CREATE TABLE IF NOT EXISTS leg_usage_records (
			lur_key TEXT PRIMARY KEY,
			tur_key TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			a_leg_id TEXT NOT NULL,
			b_leg_id TEXT NOT NULL,
			sequence INTEGER NOT NULL CHECK (sequence > 0),
			backend_id TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			model_id TEXT NOT NULL,
			outcome TEXT NOT NULL,
			surfaced TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			sealed_at TEXT NOT NULL,
			UNIQUE(tur_key, b_leg_id),
			UNIQUE(tur_key, sequence),
			FOREIGN KEY(tur_key) REFERENCES turn_usage_records(tur_key)
		)`,
		`CREATE TABLE IF NOT EXISTS usage_record_processing (
			tur_key TEXT PRIMARY KEY,
			tur_fingerprint TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending','processing','retryable','processed','unreconciled_cost','terminal_error')),
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_until TEXT,
			retry_count INTEGER NOT NULL CHECK (retry_count >= 0),
			safe_error_code TEXT NOT NULL DEFAULT '',
			result_ref TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL,
			FOREIGN KEY(tur_key) REFERENCES turn_usage_records(tur_key)
		)`,
		`CREATE TABLE IF NOT EXISTS journal_transactions (
			transaction_id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			book TEXT NOT NULL CHECK (book IN ('financial','authorization')),
			currency TEXT NOT NULL,
			source_key TEXT NOT NULL,
			semantic_fingerprint TEXT NOT NULL,
			turn_id TEXT NOT NULL DEFAULT '',
			a_leg_id TEXT NOT NULL DEFAULT '',
			b_leg_id TEXT NOT NULL DEFAULT '',
			account_sequence INTEGER NOT NULL CHECK (account_sequence > 0),
			reversal_of TEXT NOT NULL DEFAULT '',
			corrects_transaction_id TEXT NOT NULL DEFAULT '',
			correction_group_id TEXT NOT NULL DEFAULT '',
			recorded_at TEXT NOT NULL,
			UNIQUE(account_id, book, source_key),
			UNIQUE(account_id, account_sequence),
			FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
		)`,
		`CREATE TABLE IF NOT EXISTS journal_entries (
			transaction_id TEXT NOT NULL,
			ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
			ledger_account TEXT NOT NULL,
			side TEXT NOT NULL CHECK (side IN ('debit','credit')),
			currency TEXT NOT NULL,
			amount_nano INTEGER NOT NULL CHECK (amount_nano > 0),
			PRIMARY KEY(transaction_id, ordinal),
			FOREIGN KEY(transaction_id) REFERENCES journal_transactions(transaction_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_holds_account_status ON authorization_holds(account_id, status)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_processing_status ON usage_record_processing(status, updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_journal_account_sequence ON journal_transactions(account_id, account_sequence)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_journal_source ON journal_transactions(account_id, book, source_key)`,
		`CREATE TRIGGER IF NOT EXISTS billing_policy_events_immutable_update BEFORE UPDATE ON billing_account_policy_events BEGIN SELECT RAISE(ABORT, 'billing policy events are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_policy_events_immutable_delete BEFORE DELETE ON billing_account_policy_events BEGIN SELECT RAISE(ABORT, 'billing policy events are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_tur_immutable_update BEFORE UPDATE ON turn_usage_records BEGIN SELECT RAISE(ABORT, 'turn usage records are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_tur_immutable_delete BEFORE DELETE ON turn_usage_records BEGIN SELECT RAISE(ABORT, 'turn usage records are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_lur_immutable_update BEFORE UPDATE ON leg_usage_records BEGIN SELECT RAISE(ABORT, 'leg usage records are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_lur_immutable_delete BEFORE DELETE ON leg_usage_records BEGIN SELECT RAISE(ABORT, 'leg usage records are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_journal_tx_immutable_update BEFORE UPDATE ON journal_transactions BEGIN SELECT RAISE(ABORT, 'journal transactions are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_journal_tx_immutable_delete BEFORE DELETE ON journal_transactions BEGIN SELECT RAISE(ABORT, 'journal transactions are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_journal_entry_immutable_update BEFORE UPDATE ON journal_entries BEGIN SELECT RAISE(ABORT, 'journal entries are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_journal_entry_immutable_delete BEFORE DELETE ON journal_entries BEGIN SELECT RAISE(ABORT, 'journal entries are immutable'); END`,
	}
}

func postgresDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS billing_accounts (
			account_id TEXT PRIMARY KEY, currency TEXT NOT NULL,
			mode TEXT NOT NULL CHECK (mode IN ('prepaid','postpaid')),
			credit_limit_nano BIGINT NOT NULL CHECK (credit_limit_nano >= 0),
			balance_nano BIGINT NOT NULL, reserved_nano BIGINT NOT NULL CHECK (reserved_nano >= 0),
			version BIGINT NOT NULL CHECK (version >= 0),
			state TEXT NOT NULL CHECK (state IN ('ready','reconcile_required')),
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			CHECK (mode <> 'prepaid' OR credit_limit_nano = 0)
		)`,
		`CREATE TABLE IF NOT EXISTS billing_account_policy_events (
			id BIGSERIAL PRIMARY KEY, account_id TEXT NOT NULL, event_key TEXT NOT NULL,
			mode TEXT NOT NULL, currency TEXT NOT NULL, credit_limit_nano BIGINT NOT NULL CHECK (credit_limit_nano >= 0),
			effective_at TEXT NOT NULL, source_key TEXT NOT NULL, fingerprint TEXT NOT NULL,
			payload_json TEXT NOT NULL, created_at TEXT NOT NULL,
			UNIQUE(account_id, event_key), UNIQUE(account_id, source_key),
			FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
		)`,
		`CREATE TABLE IF NOT EXISTS authorization_holds (			hold_key TEXT PRIMARY KEY, authorization_id TEXT NOT NULL, account_id TEXT NOT NULL, tur_key TEXT NOT NULL,
			currency TEXT NOT NULL, amount_nano BIGINT NOT NULL CHECK (amount_nano >= 0),
			status TEXT NOT NULL CHECK (status IN ('open','closed')), source_key TEXT NOT NULL,
			fingerprint TEXT NOT NULL, pricing_ref TEXT NOT NULL DEFAULT '', charge_policy_ref TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL DEFAULT '', balance_before_nano BIGINT NOT NULL DEFAULT 0, balance_after_nano BIGINT NOT NULL DEFAULT 0,
			reserved_before_nano BIGINT NOT NULL DEFAULT 0, reserved_after_nano BIGINT NOT NULL DEFAULT 0,
			spendable_before_nano BIGINT NOT NULL DEFAULT 0, spendable_after_nano BIGINT NOT NULL DEFAULT 0,
			credit_floor_nano BIGINT NOT NULL DEFAULT 0, credit_limit_nano BIGINT NOT NULL DEFAULT 0,
			version_before BIGINT NOT NULL DEFAULT 0, version_after BIGINT NOT NULL DEFAULT 0,
			closed_reason TEXT NOT NULL DEFAULT '', expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL, closed_at TEXT,
			UNIQUE(account_id, tur_key), UNIQUE(account_id, source_key),
			FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
		)`,
		`CREATE TABLE IF NOT EXISTS turn_usage_records (
			tur_key TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, schema_version INTEGER NOT NULL CHECK (schema_version > 0),
			account_id TEXT NOT NULL, turn_id TEXT NOT NULL, a_leg_id TEXT NOT NULL, authorization_id TEXT NOT NULL,
			started_at TEXT NOT NULL, finished_at TEXT NOT NULL, outcome TEXT NOT NULL,
			customer_pricing_ref TEXT NOT NULL, charge_policy_ref TEXT NOT NULL, payload_json TEXT NOT NULL, sealed_at TEXT NOT NULL,
			UNIQUE(account_id, turn_id), FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
		)`,
		`CREATE TABLE IF NOT EXISTS leg_usage_records (
			lur_key TEXT PRIMARY KEY, tur_key TEXT NOT NULL, fingerprint TEXT NOT NULL, a_leg_id TEXT NOT NULL, b_leg_id TEXT NOT NULL,
			sequence INTEGER NOT NULL CHECK (sequence > 0), backend_id TEXT NOT NULL, provider_id TEXT NOT NULL, model_id TEXT NOT NULL,
			outcome TEXT NOT NULL, surfaced TEXT NOT NULL, payload_json TEXT NOT NULL, sealed_at TEXT NOT NULL,
			UNIQUE(tur_key, b_leg_id), UNIQUE(tur_key, sequence), FOREIGN KEY(tur_key) REFERENCES turn_usage_records(tur_key)
		)`,
		`CREATE TABLE IF NOT EXISTS usage_record_processing (
			tur_key TEXT PRIMARY KEY, tur_fingerprint TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending','processing','retryable','processed','unreconciled_cost','terminal_error')),
			lease_owner TEXT NOT NULL DEFAULT '', lease_until TEXT, retry_count INTEGER NOT NULL CHECK (retry_count >= 0),
			safe_error_code TEXT NOT NULL DEFAULT '', result_ref TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL,
			FOREIGN KEY(tur_key) REFERENCES turn_usage_records(tur_key)
		)`,
		`CREATE TABLE IF NOT EXISTS journal_transactions (
			transaction_id TEXT PRIMARY KEY, account_id TEXT NOT NULL,
			book TEXT NOT NULL CHECK (book IN ('financial','authorization')), currency TEXT NOT NULL,
			source_key TEXT NOT NULL, semantic_fingerprint TEXT NOT NULL, turn_id TEXT NOT NULL DEFAULT '',
			a_leg_id TEXT NOT NULL DEFAULT '', b_leg_id TEXT NOT NULL DEFAULT '', account_sequence BIGINT NOT NULL CHECK (account_sequence > 0),
			reversal_of TEXT NOT NULL DEFAULT '', corrects_transaction_id TEXT NOT NULL DEFAULT '', correction_group_id TEXT NOT NULL DEFAULT '',
			recorded_at TEXT NOT NULL, UNIQUE(account_id, book, source_key), UNIQUE(account_id, account_sequence),
			FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
		)`,
		`CREATE TABLE IF NOT EXISTS journal_entries (
			transaction_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK (ordinal >= 0), ledger_account TEXT NOT NULL,
			side TEXT NOT NULL CHECK (side IN ('debit','credit')), currency TEXT NOT NULL, amount_nano BIGINT NOT NULL CHECK (amount_nano > 0),
			PRIMARY KEY(transaction_id, ordinal), FOREIGN KEY(transaction_id) REFERENCES journal_transactions(transaction_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_holds_account_status ON authorization_holds(account_id, status)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_processing_status ON usage_record_processing(status, updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_journal_account_sequence ON journal_transactions(account_id, account_sequence)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_journal_source ON journal_transactions(account_id, book, source_key)`,
		`CREATE OR REPLACE FUNCTION billing_reject_immutable() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'billing immutable row'; END; $$ LANGUAGE plpgsql`,
		`CREATE OR REPLACE FUNCTION billing_reject_immutable_entry() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'billing immutable entry'; END; $$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS billing_policy_events_immutable ON billing_account_policy_events`,
		`CREATE TRIGGER billing_policy_events_immutable BEFORE UPDATE OR DELETE ON billing_account_policy_events FOR EACH ROW EXECUTE FUNCTION billing_reject_immutable()`,
		`DROP TRIGGER IF EXISTS billing_tur_immutable ON turn_usage_records`,
		`CREATE TRIGGER billing_tur_immutable BEFORE UPDATE OR DELETE ON turn_usage_records FOR EACH ROW EXECUTE FUNCTION billing_reject_immutable()`,
		`DROP TRIGGER IF EXISTS billing_lur_immutable ON leg_usage_records`,
		`CREATE TRIGGER billing_lur_immutable BEFORE UPDATE OR DELETE ON leg_usage_records FOR EACH ROW EXECUTE FUNCTION billing_reject_immutable()`,
		`DROP TRIGGER IF EXISTS billing_journal_tx_immutable ON journal_transactions`,
		`CREATE TRIGGER billing_journal_tx_immutable BEFORE UPDATE OR DELETE ON journal_transactions FOR EACH ROW EXECUTE FUNCTION billing_reject_immutable()`,
		`DROP TRIGGER IF EXISTS billing_journal_entry_immutable ON journal_entries`,
		`CREATE TRIGGER billing_journal_entry_immutable BEFORE UPDATE OR DELETE ON journal_entries FOR EACH ROW EXECUTE FUNCTION billing_reject_immutable_entry()`,
	}
}
