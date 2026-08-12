package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const Phase6MigrationName = "20260815000000"

func registerPhase6Migration() {
	migrations.MustRegister(phase6SchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func phase6SchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing phase6 schema: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements := []string{
			`ALTER TABLE billing_accounts ADD COLUMN opening_balance_nano INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE billing_operation_snapshots ADD COLUMN integrity_fingerprint TEXT NOT NULL DEFAULT ''`,
			`UPDATE billing_accounts SET opening_balance_nano = balance_nano - COALESCE((SELECT SUM(CASE WHEN e.ledger_account = 'customer_financial_account' AND e.side = 'credit' THEN e.amount_nano WHEN e.ledger_account = 'customer_financial_account' AND e.side = 'debit' THEN -e.amount_nano ELSE 0 END) FROM journal_entries e JOIN journal_transactions j ON j.transaction_id = e.transaction_id WHERE j.account_id = billing_accounts.account_id AND j.book = 'financial'), 0)`,
			`CREATE TRIGGER IF NOT EXISTS billing_operation_snapshots_immutable_update BEFORE UPDATE ON billing_operation_snapshots BEGIN SELECT RAISE(ABORT, 'billing operation snapshots are immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS billing_operation_snapshots_immutable_delete BEFORE DELETE ON billing_operation_snapshots BEGIN SELECT RAISE(ABORT, 'billing operation snapshots are immutable'); END`,
			`CREATE TABLE IF NOT EXISTS billing_account_openings (
				account_id TEXT PRIMARY KEY,
				opening_balance_nano INTEGER NOT NULL,
				currency TEXT NOT NULL,
				mode TEXT NOT NULL,
				credit_limit_nano INTEGER NOT NULL,
				fingerprint TEXT NOT NULL,
				created_at TEXT NOT NULL,
				FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
			)`,
			`CREATE TRIGGER IF NOT EXISTS billing_account_openings_immutable_update BEFORE UPDATE ON billing_account_openings BEGIN SELECT RAISE(ABORT, 'billing account openings are immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS billing_account_openings_immutable_delete BEFORE DELETE ON billing_account_openings BEGIN SELECT RAISE(ABORT, 'billing account openings are immutable'); END`,
			`CREATE TABLE IF NOT EXISTS billing_reconciliation_events (
				event_key TEXT PRIMARY KEY,
				account_id TEXT NOT NULL,
				from_state TEXT NOT NULL,
				to_state TEXT NOT NULL,
				first_mismatch_sequence INTEGER NOT NULL DEFAULT 0,
				balance_nano INTEGER NOT NULL,
				reserved_nano INTEGER NOT NULL,
				spendable_nano INTEGER NOT NULL,
				created_at TEXT NOT NULL,
				UNIQUE(account_id, event_key),
				FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
			)`,
			`CREATE TRIGGER IF NOT EXISTS billing_reconciliation_events_immutable_update BEFORE UPDATE ON billing_reconciliation_events BEGIN SELECT RAISE(ABORT, 'billing reconciliation events are immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS billing_reconciliation_events_immutable_delete BEFORE DELETE ON billing_reconciliation_events BEGIN SELECT RAISE(ABORT, 'billing reconciliation events are immutable'); END`,
			`INSERT OR IGNORE INTO billing_account_openings(account_id, opening_balance_nano, currency, mode, credit_limit_nano, fingerprint, created_at) SELECT account_id, opening_balance_nano, currency, mode, credit_limit_nano, 'legacy-opening:v1:' || account_id, created_at FROM billing_accounts`,
		}
		for _, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				return fmt.Errorf("billing phase6 sqlite DDL: %w", err)
			}
		}
	case dialect.PG:
		statements := []string{
			`ALTER TABLE billing_accounts ADD COLUMN IF NOT EXISTS opening_balance_nano BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE billing_operation_snapshots ADD COLUMN IF NOT EXISTS integrity_fingerprint TEXT NOT NULL DEFAULT ''`,
			`UPDATE billing_accounts SET opening_balance_nano = balance_nano - COALESCE((SELECT SUM(CASE WHEN e.ledger_account = 'customer_financial_account' AND e.side = 'credit' THEN e.amount_nano WHEN e.ledger_account = 'customer_financial_account' AND e.side = 'debit' THEN -e.amount_nano ELSE 0 END) FROM journal_entries e JOIN journal_transactions j ON j.transaction_id = e.transaction_id WHERE j.account_id = billing_accounts.account_id AND j.book = 'financial'), 0)`,
			`DROP TRIGGER IF EXISTS billing_operation_snapshots_immutable ON billing_operation_snapshots`,
			`CREATE TRIGGER billing_operation_snapshots_immutable BEFORE UPDATE OR DELETE ON billing_operation_snapshots FOR EACH ROW EXECUTE FUNCTION billing_reject_immutable()`,
			`CREATE TABLE IF NOT EXISTS billing_account_openings (
				account_id TEXT PRIMARY KEY,
				opening_balance_nano BIGINT NOT NULL,
				currency TEXT NOT NULL,
				mode TEXT NOT NULL,
				credit_limit_nano BIGINT NOT NULL,
				fingerprint TEXT NOT NULL,
				created_at TEXT NOT NULL,
				FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
			)`,
			`CREATE OR REPLACE FUNCTION billing_reject_opening_immutable() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'billing account openings are immutable'; END; $$ LANGUAGE plpgsql`,
			`DROP TRIGGER IF EXISTS billing_account_openings_immutable ON billing_account_openings`,
			`CREATE TRIGGER billing_account_openings_immutable BEFORE UPDATE OR DELETE ON billing_account_openings FOR EACH ROW EXECUTE FUNCTION billing_reject_opening_immutable()`,
			`CREATE TABLE IF NOT EXISTS billing_reconciliation_events (
				event_key TEXT PRIMARY KEY, account_id TEXT NOT NULL, from_state TEXT NOT NULL, to_state TEXT NOT NULL,
				first_mismatch_sequence BIGINT NOT NULL DEFAULT 0, balance_nano BIGINT NOT NULL,
				reserved_nano BIGINT NOT NULL, spendable_nano BIGINT NOT NULL, created_at TEXT NOT NULL,
				UNIQUE(account_id, event_key), FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
			)`,
			`CREATE OR REPLACE FUNCTION billing_reject_reconciliation_immutable() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'billing reconciliation events are immutable'; END; $$ LANGUAGE plpgsql`,
			`DROP TRIGGER IF EXISTS billing_reconciliation_events_immutable ON billing_reconciliation_events`,
			`CREATE TRIGGER billing_reconciliation_events_immutable BEFORE UPDATE OR DELETE ON billing_reconciliation_events FOR EACH ROW EXECUTE FUNCTION billing_reject_reconciliation_immutable()`,
			`INSERT INTO billing_account_openings(account_id, opening_balance_nano, currency, mode, credit_limit_nano, fingerprint, created_at) SELECT account_id, opening_balance_nano, currency, mode, credit_limit_nano, 'legacy-opening:v1:' || account_id, created_at FROM billing_accounts ON CONFLICT (account_id) DO NOTHING`,
		}
		for _, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("billing phase6 postgres DDL: %w", err)
			}
		}
	default:
		return fmt.Errorf("billing phase6 schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	return nil
}
