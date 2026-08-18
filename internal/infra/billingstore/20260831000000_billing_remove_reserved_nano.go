package billingstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// ReservedColumnRemovalMigrationName is the forward schema contract for the
// current account model. Historical migration sources retain their original
// reserved column definitions; only this migration removes the materialized
// account column. Journal and operation-snapshot reserved columns remain
// immutable audit storage so old authorization rows and snapshots retain exact
// evidence; current DTOs omit them and current writers write proven zero values.
const ReservedColumnRemovalMigrationName = "20260831000000"

func registerReservedColumnRemovalMigration() {
	migrations.MustRegister(reservedColumnRemovalSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func reservedColumnRemovalSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing reserved-nano removal: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite:
		return removeSQLiteReservedColumn(ctx, db)
	case dialect.PG:
		return removePostgresReservedColumn(ctx, db)
	default:
		return fmt.Errorf("billing reserved-nano removal: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}

func removePostgresReservedColumn(ctx context.Context, db *bun.DB) (retErr error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billing reserved-nano removal PostgreSQL begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `LOCK TABLE billing_accounts IN ACCESS EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("billing reserved-nano removal PostgreSQL lock: %w", err)
	}
	var residue int
	if err := tx.NewRaw(`SELECT COUNT(1) FROM billing_accounts WHERE reserved_nano <> 0`).Scan(ctx, &residue); err != nil {
		if isMissingColumn(err) {
			return nil
		}
		return fmt.Errorf("billing reserved-nano removal PostgreSQL residue proof: %w", err)
	}
	if residue != 0 {
		return fmt.Errorf("billing reserved-nano removal: refusing to discard %d nonzero account value(s)", residue)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE billing_accounts DROP COLUMN IF EXISTS reserved_nano`); err != nil {
		return fmt.Errorf("billing reserved-nano removal PostgreSQL DDL: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billing reserved-nano removal PostgreSQL commit: %w", err)
	}
	return nil
}

func removeSQLiteReservedColumn(ctx context.Context, db *bun.DB) (retErr error) {
	conn, err := db.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("billing reserved-nano removal SQLite connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	var hasColumn int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(1) FROM pragma_table_info('billing_accounts') WHERE name = 'reserved_nano'`).Scan(&hasColumn); err != nil {
		return fmt.Errorf("billing reserved-nano removal SQLite column probe: %w", err)
	}
	if hasColumn == 0 {
		return nil
	}
	var foreignKeys int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return fmt.Errorf("billing reserved-nano removal SQLite foreign-key probe: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("billing reserved-nano removal SQLite disable foreign keys: %w", err)
	}
	defer func() {
		if _, err := conn.ExecContext(context.Background(), fmt.Sprintf(`PRAGMA foreign_keys = %d`, foreignKeys)); err != nil && retErr == nil {
			retErr = fmt.Errorf("billing reserved-nano removal SQLite restore foreign keys: %w", err)
		}
	}()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("billing reserved-nano removal SQLite begin immediate: %w", err)
	}
	inTx := true
	defer func() {
		if retErr != nil && inTx {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	var residue int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(1) FROM billing_accounts WHERE reserved_nano <> 0`).Scan(&residue); err != nil {
		retErr = fmt.Errorf("billing reserved-nano removal SQLite residue proof: %w", err)
		return retErr
	}
	if residue != 0 {
		retErr = fmt.Errorf("billing reserved-nano removal: refusing to discard %d nonzero account value(s)", residue)
		return retErr
	}
	for _, statement := range []string{
		`CREATE TABLE billing_accounts_phase4 (
			account_id TEXT PRIMARY KEY,
			currency TEXT NOT NULL,
			mode TEXT NOT NULL CHECK (mode IN ('prepaid','postpaid')),
			credit_limit_nano INTEGER NOT NULL CHECK (credit_limit_nano >= 0),
			balance_nano INTEGER NOT NULL,
			opening_balance_nano INTEGER NOT NULL,
			version INTEGER NOT NULL CHECK (version >= 0),
			state TEXT NOT NULL CHECK (state IN ('ready','reconcile_required')),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			CHECK (mode <> 'prepaid' OR credit_limit_nano = 0)
		)`,
		`INSERT INTO billing_accounts_phase4(account_id, currency, mode, credit_limit_nano, balance_nano, opening_balance_nano, version, state, created_at, updated_at) SELECT account_id, currency, mode, credit_limit_nano, balance_nano, opening_balance_nano, version, state, created_at, updated_at FROM billing_accounts`,
		`DROP TABLE billing_accounts`,
		`ALTER TABLE billing_accounts_phase4 RENAME TO billing_accounts`,
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			retErr = fmt.Errorf("billing reserved-nano removal SQLite DDL: %w", err)
			return retErr
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		retErr = fmt.Errorf("billing reserved-nano removal SQLite commit: %w", err)
		return retErr
	}
	inTx = false
	return nil
}

func isMissingColumn(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return text == sql.ErrNoRows.Error() || containsFold(text, "no such column") || containsFold(text, "undefined column")
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && (stringIndexFold(s, sub) >= 0)
}

func stringIndexFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := range sub {
			if lowerASCII(s[i+j]) != lowerASCII(sub[j]) {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}
