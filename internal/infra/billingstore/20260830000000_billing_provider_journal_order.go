package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const (
	ProviderJournalOrderMigrationName            = "20260830000000"
	ProviderJournalSequenceContractMigrationName = "20260830010000"
)

const (
	providerJournalOrderIndex     = "idx_billing_journal_provider_order"
	providerJournalBookOrderIndex = "idx_billing_journal_book_provider_order"
	providerJournalSequenceCheck  = "billing_journal_sequence_contract"
)

func registerProviderJournalOrderMigration() {
	migrations.MustRegister(providerJournalOrderSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func providerJournalOrderSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing provider journal order schema: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.PG:
		for _, statement := range []string{
			`ALTER TABLE journal_transactions ALTER COLUMN account_sequence DROP NOT NULL`,
			`ALTER TABLE journal_transactions ALTER COLUMN recorded_at SET DEFAULT CURRENT_TIMESTAMP`,
			`CREATE INDEX IF NOT EXISTS ` + providerJournalOrderIndex + ` ON journal_transactions(account_id, recorded_at, transaction_id)`,
			`CREATE INDEX IF NOT EXISTS ` + providerJournalBookOrderIndex + ` ON journal_transactions(book, currency, recorded_at, transaction_id)`,
		} {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("billing provider journal order PostgreSQL DDL: %w", err)
			}
		}
		return addProviderSequenceConstraintPG(ctx, db)
	case dialect.SQLite:
		return providerJournalOrderSQLite(ctx, db)
	default:
		return fmt.Errorf("billing provider journal order schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}

func addProviderSequenceConstraintPG(ctx context.Context, db *bun.DB) error {
	var count int
	if err := db.NewRaw(`SELECT COUNT(1) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'journal_transactions' AND c.conname = ?`, providerJournalSequenceCheck).Scan(ctx, &count); err != nil {
		return fmt.Errorf("billing provider journal sequence contract probe: %w", err)
	}
	if count == 0 {
		if _, err := db.ExecContext(ctx, `ALTER TABLE journal_transactions ADD CONSTRAINT `+providerJournalSequenceCheck+` CHECK ((operation_kind = 'provider_call_cogs' AND (account_sequence IS NULL OR account_sequence > 0)) OR (operation_kind <> 'provider_call_cogs' AND account_sequence > 0))`); err != nil {
			return fmt.Errorf("billing provider journal sequence contract: %w", err)
		}
	}
	return nil
}

func providerJournalSequenceContractSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing provider journal sequence contract: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.PG:
		return addProviderSequenceConstraintPG(ctx, db)
	case dialect.SQLite:
		var ddl string
		if err := db.NewRaw(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'journal_transactions'`).Scan(ctx, &ddl); err != nil {
			return fmt.Errorf("billing provider journal sequence contract SQLite probe: %w", err)
		}
		if strings.Contains(strings.ToLower(ddl), "operation_kind = 'provider_call_cogs'") && strings.Contains(strings.ToLower(ddl), "operation_kind <> 'provider_call_cogs'") {
			return nil
		}
		return providerJournalOrderSQLite(ctx, db)
	default:
		return fmt.Errorf("billing provider journal sequence contract: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}

func providerJournalOrderSQLite(ctx context.Context, db *bun.DB) (retErr error) {
	// SQLite PRAGMAs are connection-local and SQLite's destructive table
	// rebuild must not be interleaved with pooled readers/writers. Reserve one
	// connection, disable foreign keys before the transaction, and execute the
	// complete rebuild on that same connection.
	conn, err := db.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("billing provider journal order SQLite connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	var foreignKeys int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return fmt.Errorf("billing provider journal order SQLite foreign-key probe: %w", err)
	}
	restoreForeignKeys := func() error {
		_, err := conn.ExecContext(context.Background(), fmt.Sprintf("PRAGMA foreign_keys = %d", foreignKeys))
		return err
	}
	defer func() {
		if err := restoreForeignKeys(); err != nil {
			if retErr == nil {
				retErr = fmt.Errorf("billing provider journal order SQLite restore foreign_keys: %w", err)
			} else {
				retErr = fmt.Errorf("%v; restore foreign_keys: %w", retErr, err)
			}
		}
	}()

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("billing provider journal order SQLite disable foreign keys: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("billing provider journal order SQLite begin immediate: %w", err)
	}
	inTransaction := true
	rollback := func() {
		if inTransaction {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
			inTransaction = false
		}
	}
	defer func() {
		if retErr != nil {
			rollback()
		}
	}()

	statements := []string{
		`DROP INDEX IF EXISTS idx_billing_journal_account_sequence`,
		`DROP INDEX IF EXISTS idx_billing_journal_source`,
		`DROP INDEX IF EXISTS ` + journalReversalUniqueIndex,
		`DROP TRIGGER IF EXISTS billing_journal_tx_immutable_update`,
		`DROP TRIGGER IF EXISTS billing_journal_tx_immutable_delete`,
		`CREATE TABLE journal_transactions_phase3 (
			transaction_id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			book TEXT NOT NULL CHECK (book IN ('financial','authorization')),
			currency TEXT NOT NULL,
			source_key TEXT NOT NULL,
			semantic_fingerprint TEXT NOT NULL,
			turn_id TEXT NOT NULL DEFAULT '',
			a_leg_id TEXT NOT NULL DEFAULT '',
			b_leg_id TEXT NOT NULL DEFAULT '',
			account_sequence INTEGER NULL CHECK (account_sequence IS NULL OR account_sequence > 0),
			reversal_of TEXT NOT NULL DEFAULT '',
			corrects_transaction_id TEXT NOT NULL DEFAULT '',
			correction_group_id TEXT NOT NULL DEFAULT '',
			operation_kind TEXT NOT NULL DEFAULT '',
			balance_before_nano INTEGER NOT NULL DEFAULT 0,
			balance_after_nano INTEGER NOT NULL DEFAULT 0,
			reserved_before_nano INTEGER NOT NULL DEFAULT 0,
			reserved_after_nano INTEGER NOT NULL DEFAULT 0,
			spendable_before_nano INTEGER NOT NULL DEFAULT 0,
			spendable_after_nano INTEGER NOT NULL DEFAULT 0,
			credit_floor_nano INTEGER NOT NULL DEFAULT 0,
			credit_limit_nano INTEGER NOT NULL DEFAULT 0,
			mode TEXT NOT NULL DEFAULT '',
			snapshot_version_before INTEGER NOT NULL DEFAULT 0,
			snapshot_version_after INTEGER NOT NULL DEFAULT 0,
			recorded_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			UNIQUE(account_id, book, source_key),
			UNIQUE(account_id, account_sequence),
			CHECK ((operation_kind = 'provider_call_cogs' AND (account_sequence IS NULL OR account_sequence > 0)) OR (operation_kind <> 'provider_call_cogs' AND account_sequence > 0)),
			FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
		)`,
		`INSERT INTO journal_transactions_phase3(transaction_id, account_id, book, currency, source_key, semantic_fingerprint, turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id, correction_group_id, operation_kind, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at)
			SELECT transaction_id, account_id, book, currency, source_key, semantic_fingerprint, turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id, correction_group_id, operation_kind, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at FROM journal_transactions`,
		`DROP TABLE journal_transactions`,
		`ALTER TABLE journal_transactions_phase3 RENAME TO journal_transactions`,
		`CREATE INDEX IF NOT EXISTS idx_billing_journal_account_sequence ON journal_transactions(account_id, account_sequence)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_journal_source ON journal_transactions(account_id, book, source_key)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + journalReversalUniqueIndex + ` ON journal_transactions(account_id, book, reversal_of) WHERE reversal_of <> ''`,
		`CREATE INDEX IF NOT EXISTS ` + providerJournalOrderIndex + ` ON journal_transactions(account_id, recorded_at, transaction_id)`,
		`CREATE INDEX IF NOT EXISTS ` + providerJournalBookOrderIndex + ` ON journal_transactions(book, currency, recorded_at, transaction_id)`,
		`CREATE TRIGGER billing_journal_tx_immutable_update BEFORE UPDATE ON journal_transactions BEGIN SELECT RAISE(ABORT, 'journal transactions are immutable'); END`,
		`CREATE TRIGGER billing_journal_tx_immutable_delete BEFORE DELETE ON journal_transactions BEGIN SELECT RAISE(ABORT, 'journal transactions are immutable'); END`,
	}
	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			retErr = fmt.Errorf("billing provider journal order SQLite DDL: %w", err)
			return retErr
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		retErr = fmt.Errorf("billing provider journal order SQLite commit: %w", err)
		return retErr
	}
	inTransaction = false
	return nil
}
