package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingAllocationMigrationName identifies the additive immutable allocation
// history. Allocation rows are source-preserving derived economics, not money
// journal entries or provider request debits.
const BillingAllocationMigrationName = "20260918000000"

const (
	billingAllocationSourceIndex = "idx_billing_allocations_source"
	billingAllocationTargetIndex = "idx_billing_allocation_targets_target"
)

func registerBillingAllocationMigration() {
	migrations.MustRegister(billingAllocationSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingAllocationSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing allocation schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = billingAllocationSQLiteDDL()
	case dialect.PG:
		statements = billingAllocationPostgresDDL()
	default:
		return fmt.Errorf("billing allocation schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing allocation DDL: %w", err)
		}
	}
	return nil
}

func billingAllocationSQLiteDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS billing_allocation_store_locks (
			store_id TEXT NOT NULL PRIMARY KEY
		)`,
		`CREATE TABLE IF NOT EXISTS billing_allocations (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			allocation_id TEXT NOT NULL,
			allocation_version INTEGER NOT NULL CHECK (allocation_version > 0),
			revision INTEGER NOT NULL CHECK (revision > 0),
			source_subject_kind TEXT NOT NULL,
			source_subject_id TEXT NOT NULL,
			source_subject_json TEXT NOT NULL,
			source_tenant_id TEXT NOT NULL DEFAULT '',
			source_account_id TEXT NOT NULL DEFAULT '',
			source_period_id TEXT NOT NULL DEFAULT '',
			source_basis TEXT NOT NULL,
			source_amount_coefficient TEXT NOT NULL DEFAULT '',
			source_amount_scale INTEGER NOT NULL DEFAULT 0,
			source_amount_present INTEGER NOT NULL DEFAULT 0,
			source_quantity_coefficient TEXT NOT NULL DEFAULT '',
			source_quantity_scale INTEGER NOT NULL DEFAULT 0,
			source_quantity_present INTEGER NOT NULL DEFAULT 0,
			currency TEXT NOT NULL DEFAULT '',
			unit TEXT NOT NULL DEFAULT '',
			policy_method TEXT NOT NULL,
			policy_version TEXT NOT NULL,
			policy_hash TEXT NOT NULL,
			operation TEXT NOT NULL,
			rounding_scope TEXT NOT NULL,
			rounding_policy TEXT NOT NULL,
			rounding_residual_policy TEXT NOT NULL,
			rounded_source_nano INTEGER NOT NULL DEFAULT 0,
			rounded_source_currency TEXT NOT NULL DEFAULT '',
			rounded_source_present INTEGER NOT NULL DEFAULT 0,
			rounding_residual_nano INTEGER NOT NULL DEFAULT 0,
			source_observation_refs_json TEXT NOT NULL DEFAULT '[]',
			source_valuation_refs_json TEXT NOT NULL DEFAULT '[]',
			supersedes_json TEXT NOT NULL DEFAULT '[]',
			canonical_json TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			projection_version INTEGER NOT NULL DEFAULT 1,
			created_at_unix INTEGER NOT NULL,
			UNIQUE(store_id, allocation_id, allocation_version)
		)`,
		`CREATE TABLE IF NOT EXISTS billing_allocation_targets (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			allocation_id TEXT NOT NULL,
			allocation_version INTEGER NOT NULL,
			target_id TEXT NOT NULL,
			target_kind TEXT NOT NULL DEFAULT '',
			target_subject_id TEXT NOT NULL DEFAULT '',
			target_json TEXT NOT NULL DEFAULT '{}',
			unallocated INTEGER NOT NULL DEFAULT 0,
			informational INTEGER NOT NULL DEFAULT 0,
			account_id TEXT NOT NULL DEFAULT '',
			period_id TEXT NOT NULL DEFAULT '',
			currency TEXT NOT NULL DEFAULT '',
			unit TEXT NOT NULL DEFAULT '',
			weight_numerator TEXT NOT NULL,
			weight_denominator TEXT NOT NULL,
			share_numerator TEXT NOT NULL,
			share_denominator TEXT NOT NULL,
			rounded_nano INTEGER NOT NULL DEFAULT 0,
			rounded_currency TEXT NOT NULL DEFAULT '',
			rounded_present INTEGER NOT NULL DEFAULT 0,
			rounding_residual_nano INTEGER NOT NULL DEFAULT 0,
			projection_version INTEGER NOT NULL DEFAULT 1,
			UNIQUE(store_id, allocation_id, allocation_version, target_id),
			FOREIGN KEY(store_id, allocation_id, allocation_version) REFERENCES billing_allocations(store_id, allocation_id, allocation_version)
		)`,
		`CREATE INDEX IF NOT EXISTS ` + billingAllocationSourceIndex + ` ON billing_allocations(store_id, source_subject_kind, source_subject_id, created_at_unix, allocation_id, allocation_version, id)`,
		`CREATE INDEX IF NOT EXISTS ` + billingAllocationTargetIndex + ` ON billing_allocation_targets(store_id, target_kind, target_subject_id, allocation_id, allocation_version, target_id)`,
		`CREATE TRIGGER IF NOT EXISTS billing_allocations_immutable_update BEFORE UPDATE ON billing_allocations BEGIN SELECT RAISE(ABORT, 'billing allocations are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_allocations_immutable_delete BEFORE DELETE ON billing_allocations BEGIN SELECT RAISE(ABORT, 'billing allocations are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_allocation_targets_immutable_update BEFORE UPDATE ON billing_allocation_targets BEGIN SELECT RAISE(ABORT, 'billing allocation targets are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_allocation_targets_immutable_delete BEFORE DELETE ON billing_allocation_targets BEGIN SELECT RAISE(ABORT, 'billing allocation targets are immutable'); END`,
	}
}

func billingAllocationPostgresDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS billing_allocation_store_locks (
			store_id TEXT NOT NULL PRIMARY KEY
		)`,
		`CREATE TABLE IF NOT EXISTS billing_allocations (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL,
			allocation_id TEXT NOT NULL,
			allocation_version BIGINT NOT NULL CHECK (allocation_version > 0),
			revision BIGINT NOT NULL CHECK (revision > 0),
			source_subject_kind TEXT NOT NULL,
			source_subject_id TEXT NOT NULL,
			source_subject_json TEXT NOT NULL,
			source_tenant_id TEXT NOT NULL DEFAULT '', source_account_id TEXT NOT NULL DEFAULT '', source_period_id TEXT NOT NULL DEFAULT '',
			source_basis TEXT NOT NULL,
			source_amount_coefficient TEXT NOT NULL DEFAULT '', source_amount_scale INTEGER NOT NULL DEFAULT 0, source_amount_present BOOLEAN NOT NULL DEFAULT FALSE,
			source_quantity_coefficient TEXT NOT NULL DEFAULT '', source_quantity_scale INTEGER NOT NULL DEFAULT 0, source_quantity_present BOOLEAN NOT NULL DEFAULT FALSE,
			currency TEXT NOT NULL DEFAULT '', unit TEXT NOT NULL DEFAULT '',
			policy_method TEXT NOT NULL, policy_version TEXT NOT NULL, policy_hash TEXT NOT NULL,
			operation TEXT NOT NULL, rounding_scope TEXT NOT NULL, rounding_policy TEXT NOT NULL, rounding_residual_policy TEXT NOT NULL,
			rounded_source_nano BIGINT NOT NULL DEFAULT 0, rounded_source_currency TEXT NOT NULL DEFAULT '', rounded_source_present BOOLEAN NOT NULL DEFAULT FALSE,
			rounding_residual_nano BIGINT NOT NULL DEFAULT 0,
			source_observation_refs_json TEXT NOT NULL DEFAULT '[]', source_valuation_refs_json TEXT NOT NULL DEFAULT '[]', supersedes_json TEXT NOT NULL DEFAULT '[]',
			canonical_json TEXT NOT NULL, fingerprint TEXT NOT NULL, projection_version INTEGER NOT NULL DEFAULT 1, created_at_unix BIGINT NOT NULL,
			UNIQUE(store_id, allocation_id, allocation_version)
		)`,
		`CREATE TABLE IF NOT EXISTS billing_allocation_targets (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL, allocation_id TEXT NOT NULL, allocation_version BIGINT NOT NULL, target_id TEXT NOT NULL,
			target_kind TEXT NOT NULL DEFAULT '', target_subject_id TEXT NOT NULL DEFAULT '', target_json TEXT NOT NULL DEFAULT '{}',
			unallocated BOOLEAN NOT NULL DEFAULT FALSE, informational BOOLEAN NOT NULL DEFAULT FALSE,
			account_id TEXT NOT NULL DEFAULT '', period_id TEXT NOT NULL DEFAULT '', currency TEXT NOT NULL DEFAULT '', unit TEXT NOT NULL DEFAULT '',
			weight_numerator TEXT NOT NULL, weight_denominator TEXT NOT NULL, share_numerator TEXT NOT NULL, share_denominator TEXT NOT NULL,
			rounded_nano BIGINT NOT NULL DEFAULT 0, rounded_currency TEXT NOT NULL DEFAULT '', rounded_present BOOLEAN NOT NULL DEFAULT FALSE, rounding_residual_nano BIGINT NOT NULL DEFAULT 0,
			projection_version INTEGER NOT NULL DEFAULT 1,
			UNIQUE(store_id, allocation_id, allocation_version, target_id),
			FOREIGN KEY(store_id, allocation_id, allocation_version) REFERENCES billing_allocations(store_id, allocation_id, allocation_version)
		)`,
		`CREATE INDEX IF NOT EXISTS ` + billingAllocationSourceIndex + ` ON billing_allocations(store_id, source_subject_kind, source_subject_id, created_at_unix, allocation_id, allocation_version, id)`,
		`CREATE INDEX IF NOT EXISTS ` + billingAllocationTargetIndex + ` ON billing_allocation_targets(store_id, target_kind, target_subject_id, allocation_id, allocation_version, target_id)`,
		`CREATE OR REPLACE FUNCTION billing_reject_allocation_mutation() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'billing allocations are immutable'; END; $$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS billing_allocations_immutable ON billing_allocations`,
		`CREATE TRIGGER billing_allocations_immutable BEFORE UPDATE OR DELETE ON billing_allocations FOR EACH ROW EXECUTE FUNCTION billing_reject_allocation_mutation()`,
		`DROP TRIGGER IF EXISTS billing_allocation_targets_immutable ON billing_allocation_targets`,
		`CREATE TRIGGER billing_allocation_targets_immutable BEFORE UPDATE OR DELETE ON billing_allocation_targets FOR EACH ROW EXECUTE FUNCTION billing_reject_allocation_mutation()`,
	}
}
