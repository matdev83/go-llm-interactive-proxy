package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// CustomerUnitLedgerMigrationName identifies the durable customer-owned unit
// balance, operation, and reservation tables. The operation rows are immutable
// idempotency records; balance and reservation rows are the mutable authority.
const CustomerUnitLedgerMigrationName = "20260915000000"

const (
	customerUnitBalanceIdentityIndex     = "idx_billing_unit_balances_identity"
	customerUnitOperationIdentityIndex   = "idx_billing_unit_operations_identity"
	customerUnitReservationIdentityIndex = "idx_billing_unit_reservations_identity"
)

func registerCustomerUnitLedgerMigration() {
	migrations.MustRegister(customerUnitLedgerSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func customerUnitLedgerSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing customer-unit schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = customerUnitSQLiteDDL()
	case dialect.PG:
		statements = customerUnitPostgresDDL()
	default:
		return fmt.Errorf("billing customer-unit schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing customer-unit DDL: %w", err)
		}
	}
	return nil
}

func customerUnitSQLiteDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS billing_unit_balances (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			identity_key TEXT NOT NULL,
			canonical_key TEXT NOT NULL,
			account_id TEXT NOT NULL,
			pool_id TEXT NOT NULL,
			period_id TEXT NOT NULL,
			component_key TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('complete','partial','missing','conflict')),
			granted_coefficient TEXT,
			granted_scale INTEGER,
			available_coefficient TEXT,
			available_scale INTEGER,
			reserved_coefficient TEXT,
			reserved_scale INTEGER,
			consumed_coefficient TEXT,
			consumed_scale INTEGER,
			version INTEGER NOT NULL CHECK (version >= 0),
			fence INTEGER NOT NULL CHECK (fence >= 0),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(store_id, identity_key),
			CHECK ((status = 'complete' AND granted_coefficient IS NOT NULL AND available_coefficient IS NOT NULL AND reserved_coefficient IS NOT NULL AND consumed_coefficient IS NOT NULL AND version > 0 AND fence > 0) OR status <> 'complete')
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_unit_balances_identity ON billing_unit_balances(store_id, identity_key)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_unit_balances_scope ON billing_unit_balances(store_id, account_id, pool_id, period_id)`,
		`CREATE TABLE IF NOT EXISTS billing_unit_operations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			operation_id TEXT NOT NULL,
			identity_key TEXT NOT NULL,
			canonical_key TEXT NOT NULL,
			version INTEGER NOT NULL CHECK (version > 0),
			kind TEXT NOT NULL CHECK (kind IN ('grant','debit','reserve','commit','release')),
			source TEXT NOT NULL,
			reservation_id TEXT NOT NULL DEFAULT '',
			quantity_coefficient TEXT NOT NULL,
			quantity_scale INTEGER NOT NULL CHECK (quantity_scale >= 0 AND quantity_scale <= 18),
			expected_version INTEGER NOT NULL CHECK (expected_version >= 0),
			fence INTEGER NOT NULL CHECK (fence > 0),
			fallback_nano INTEGER,
			fallback_currency TEXT NOT NULL DEFAULT '',
			fingerprint TEXT NOT NULL,
			operation_json TEXT NOT NULL,
			result_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE(store_id, operation_id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_unit_operations_identity ON billing_unit_operations(store_id, operation_id)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_unit_operations_balance ON billing_unit_operations(store_id, identity_key, created_at, id)`,
		`CREATE TABLE IF NOT EXISTS billing_unit_reservations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			reservation_id TEXT NOT NULL,
			identity_key TEXT NOT NULL,
			canonical_key TEXT NOT NULL,
			quantity_coefficient TEXT NOT NULL,
			quantity_scale INTEGER NOT NULL CHECK (quantity_scale >= 0 AND quantity_scale <= 18),
			status TEXT NOT NULL CHECK (status IN ('open','committed','released')),
			source_operation_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			closed_at TEXT,
			UNIQUE(store_id, reservation_id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_unit_reservations_identity ON billing_unit_reservations(store_id, reservation_id)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_unit_reservations_balance ON billing_unit_reservations(store_id, identity_key, status, created_at, id)`,
		`CREATE TRIGGER IF NOT EXISTS billing_unit_operations_immutable_update BEFORE UPDATE ON billing_unit_operations BEGIN SELECT RAISE(ABORT, 'billing unit operations are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_unit_operations_immutable_delete BEFORE DELETE ON billing_unit_operations BEGIN SELECT RAISE(ABORT, 'billing unit operations are immutable'); END`,
	}
}

func customerUnitPostgresDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS billing_unit_balances (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL,
			identity_key TEXT NOT NULL,
			canonical_key TEXT NOT NULL,
			account_id TEXT NOT NULL,
			pool_id TEXT NOT NULL,
			period_id TEXT NOT NULL,
			component_key TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('complete','partial','missing','conflict')),
			granted_coefficient TEXT,
			granted_scale INTEGER,
			available_coefficient TEXT,
			available_scale INTEGER,
			reserved_coefficient TEXT,
			reserved_scale INTEGER,
			consumed_coefficient TEXT,
			consumed_scale INTEGER,
			version BIGINT NOT NULL CHECK (version >= 0),
			fence BIGINT NOT NULL CHECK (fence >= 0),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(store_id, identity_key),
			CHECK ((status = 'complete' AND granted_coefficient IS NOT NULL AND available_coefficient IS NOT NULL AND reserved_coefficient IS NOT NULL AND consumed_coefficient IS NOT NULL AND version > 0 AND fence > 0) OR status <> 'complete')
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_unit_balances_identity ON billing_unit_balances(store_id, identity_key)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_unit_balances_scope ON billing_unit_balances(store_id, account_id, pool_id, period_id)`,
		`CREATE TABLE IF NOT EXISTS billing_unit_operations (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL,
			operation_id TEXT NOT NULL,
			identity_key TEXT NOT NULL,
			canonical_key TEXT NOT NULL,
			version INTEGER NOT NULL CHECK (version > 0),
			kind TEXT NOT NULL CHECK (kind IN ('grant','debit','reserve','commit','release')),
			source TEXT NOT NULL,
			reservation_id TEXT NOT NULL DEFAULT '',
			quantity_coefficient TEXT NOT NULL,
			quantity_scale INTEGER NOT NULL CHECK (quantity_scale >= 0 AND quantity_scale <= 18),
			expected_version BIGINT NOT NULL CHECK (expected_version >= 0),
			fence BIGINT NOT NULL CHECK (fence > 0),
			fallback_nano BIGINT,
			fallback_currency TEXT NOT NULL DEFAULT '',
			fingerprint TEXT NOT NULL,
			operation_json TEXT NOT NULL,
			result_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE(store_id, operation_id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_unit_operations_identity ON billing_unit_operations(store_id, operation_id)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_unit_operations_balance ON billing_unit_operations(store_id, identity_key, created_at, id)`,
		`CREATE TABLE IF NOT EXISTS billing_unit_reservations (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL,
			reservation_id TEXT NOT NULL,
			identity_key TEXT NOT NULL,
			canonical_key TEXT NOT NULL,
			quantity_coefficient TEXT NOT NULL,
			quantity_scale INTEGER NOT NULL CHECK (quantity_scale >= 0 AND quantity_scale <= 18),
			status TEXT NOT NULL CHECK (status IN ('open','committed','released')),
			source_operation_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			closed_at TEXT,
			UNIQUE(store_id, reservation_id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_unit_reservations_identity ON billing_unit_reservations(store_id, reservation_id)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_unit_reservations_balance ON billing_unit_reservations(store_id, identity_key, status, created_at, id)`,
		`CREATE OR REPLACE FUNCTION billing_reject_unit_operation_mutation() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'billing unit operations are immutable'; END; $$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS billing_unit_operations_immutable ON billing_unit_operations`,
		`CREATE TRIGGER billing_unit_operations_immutable BEFORE UPDATE OR DELETE ON billing_unit_operations FOR EACH ROW EXECUTE FUNCTION billing_reject_unit_operation_mutation()`,
	}
}
