package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingEconomicJobQueueMigrationName identifies the additive queue-state
// extension for revision-aware separated economic jobs: job kind, dependency
// count, bounded retry reason, terminal failure timestamp and the failed
// status.
const BillingEconomicJobQueueMigrationName = "20260930000000"

const billingEconomicRevisionWorkStateStatusConstraint = "billing_economic_revision_work_state_status_contract"

func registerBillingEconomicJobQueueMigration() {
	migrations.MustRegister(billingEconomicJobQueueSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingEconomicJobQueueSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing economic job queue schema: nil database")
	}
	if ctx == nil {
		return fmt.Errorf("billing economic job queue schema: nil context")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite:
		return billingEconomicJobQueueSQLite(ctx, db)
	case dialect.PG:
		return billingEconomicJobQueuePostgres(ctx, db)
	default:
		return fmt.Errorf("billing economic job queue schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}

func billingEconomicJobQueueSQLite(ctx context.Context, db *bun.DB) (retErr error) {
	var ddl string
	if err := db.NewRaw(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'billing_economic_revision_work_state'`).Scan(ctx, &ddl); err != nil {
		return fmt.Errorf("billing economic job queue SQLite probe: %w", err)
	}
	if strings.Contains(ddl, "failed_at_unix") && strings.Contains(ddl, "'failed'") {
		return nil
	}
	// SQLite cannot widen a CHECK constraint in place. The queue state table
	// carries only mutable delivery metadata, so a guarded rebuild on one
	// reserved connection is the additive path; immutable work markers and
	// derived economics are untouched.
	conn, err := db.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("billing economic job queue SQLite connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	var foreignKeys int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return fmt.Errorf("billing economic job queue SQLite foreign-key probe: %w", err)
	}
	defer func() {
		if _, err := conn.ExecContext(context.Background(), fmt.Sprintf("PRAGMA foreign_keys = %d", foreignKeys)); err != nil && retErr == nil {
			retErr = fmt.Errorf("billing economic job queue SQLite restore foreign_keys: %w", err)
		}
	}()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("billing economic job queue SQLite disable foreign keys: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("billing economic job queue SQLite begin immediate: %w", err)
	}
	inTransaction := true
	defer func() {
		if retErr != nil && inTransaction {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	statements := []string{
		`DROP INDEX IF EXISTS ` + billingEconomicRevisionWorkStateIndex,
		`CREATE TABLE billing_economic_revision_work_state_v2 (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			work_id TEXT NOT NULL,
			work_version INTEGER NOT NULL,
			queue TEXT NOT NULL,
			head_key TEXT NOT NULL,
			work_kind TEXT NOT NULL DEFAULT '',
			dependency_count INTEGER NOT NULL DEFAULT 0 CHECK (dependency_count >= 0),
			status TEXT NOT NULL CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
			attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
			next_attempt_at_unix INTEGER NOT NULL DEFAULT 0,
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_until_unix INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			retry_reason TEXT NOT NULL DEFAULT '',
			fence INTEGER NOT NULL DEFAULT 0 CHECK (fence >= 0),
			completed_at_unix INTEGER NOT NULL DEFAULT 0,
			failed_at_unix INTEGER NOT NULL DEFAULT 0,
			created_at_unix INTEGER NOT NULL,
			updated_at_unix INTEGER NOT NULL,
			UNIQUE(store_id, work_id, work_version)
		)`,
		`INSERT INTO billing_economic_revision_work_state_v2(
			store_id, work_id, work_version, queue, head_key, work_kind, dependency_count, status, attempt_count,
			next_attempt_at_unix, lease_owner, lease_until_unix, last_error, retry_reason, fence,
			completed_at_unix, failed_at_unix, created_at_unix, updated_at_unix
		)
		SELECT store_id, work_id, work_version, queue, head_key,
			CASE WHEN queue = 'customer' THEN 'customer_rating' ELSE 'provider_rating' END,
			0, status, attempt_count, next_attempt_at_unix, lease_owner, lease_until_unix, last_error, '', fence,
			completed_at_unix, 0, created_at_unix, updated_at_unix
		FROM billing_economic_revision_work_state`,
		`DROP TABLE billing_economic_revision_work_state`,
		`ALTER TABLE billing_economic_revision_work_state_v2 RENAME TO billing_economic_revision_work_state`,
		`CREATE INDEX IF NOT EXISTS ` + billingEconomicRevisionWorkStateIndex + `
			ON billing_economic_revision_work_state(store_id, queue, status, next_attempt_at_unix, lease_until_unix, created_at_unix, work_id, work_version)`,
	}
	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing economic job queue SQLite DDL: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("billing economic job queue SQLite commit: %w", err)
	}
	inTransaction = false
	return nil
}

func billingEconomicJobQueuePostgres(ctx context.Context, db *bun.DB) error {
	for _, statement := range []string{
		`ALTER TABLE billing_economic_revision_work_state ADD COLUMN IF NOT EXISTS work_kind TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE billing_economic_revision_work_state ADD COLUMN IF NOT EXISTS dependency_count BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE billing_economic_revision_work_state ADD COLUMN IF NOT EXISTS retry_reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE billing_economic_revision_work_state ADD COLUMN IF NOT EXISTS failed_at_unix BIGINT NOT NULL DEFAULT 0`,
		`UPDATE billing_economic_revision_work_state
			SET work_kind = CASE WHEN queue = 'customer' THEN 'customer_rating' ELSE 'provider_rating' END
			WHERE work_kind = ''`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing economic job queue PostgreSQL DDL: %w", err)
		}
	}
	var statusConstraints []string
	if err := db.NewRaw(`
SELECT c.conname
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema() AND t.relname = 'billing_economic_revision_work_state'
	AND c.contype = 'c' AND pg_get_constraintdef(c.oid) LIKE '%status%'`).Scan(ctx, &statusConstraints); err != nil {
		return fmt.Errorf("billing economic job queue PostgreSQL status constraint probe: %w", err)
	}
	for _, name := range statusConstraints {
		if !safePostgresConstraintName(name) {
			return fmt.Errorf("billing economic job queue PostgreSQL unsafe constraint name %q", name)
		}
		if _, err := db.ExecContext(ctx, `ALTER TABLE billing_economic_revision_work_state DROP CONSTRAINT IF EXISTS "`+name+`"`); err != nil {
			return fmt.Errorf("billing economic job queue PostgreSQL drop status constraint: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE billing_economic_revision_work_state ADD CONSTRAINT `+billingEconomicRevisionWorkStateStatusConstraint+` CHECK (status IN ('pending', 'processing', 'completed', 'failed'))`); err != nil {
		return fmt.Errorf("billing economic job queue PostgreSQL status contract: %w", err)
	}
	return nil
}

func safePostgresConstraintName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}
