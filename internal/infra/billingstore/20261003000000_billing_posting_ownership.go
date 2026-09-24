package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingPostingOwnershipMigrationName identifies the durable per-operation
// posting ownership pins (Task 17.3B1, Migration Strategy step 6). One row per
// (store, operation kind, canonical operation key) pins exactly one posting
// owner (v1/v2) with the marker epoch/generation/version snapshot observed at
// acquisition, plus durable completion state so waking workers distinguish
// unposted pinned work from already completed effects. B2b4 direct-adjustment
// pins carry empty call_id (no call lineage); call_id therefore allows empty
// while account remains required. No terminal claim/worker/admission wiring
// lives in this migration.
const BillingPostingOwnershipMigrationName = "20261003000000"

const (
	billingPostingOwnershipPinIndex     = "idx_billing_posting_ownership_pin"
	billingPostingOwnershipAccountIndex = "idx_billing_posting_ownership_account"
	billingPostingOwnershipStatusIndex  = "idx_billing_posting_ownership_status"
)

func registerBillingPostingOwnershipMigration() {
	migrations.MustRegister(billingPostingOwnershipSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingPostingOwnershipSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing posting ownership schema: nil database")
	}
	if ctx == nil {
		return fmt.Errorf("billing posting ownership schema: nil context")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = []string{
			`CREATE TABLE IF NOT EXISTS billing_posting_ownership_pins (
				id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
				store_id TEXT NOT NULL,
				operation_kind TEXT NOT NULL CHECK (operation_kind IN ('customer_call_settlement', 'provider_charge', 'financial_adjustment')),
				operation_key TEXT NOT NULL CHECK (length(operation_key) > 0 AND length(operation_key) <= 1024),
				account_id TEXT NOT NULL CHECK (length(account_id) > 0 AND length(account_id) <= 512),
				call_id TEXT NOT NULL DEFAULT '' CHECK (length(call_id) <= 128),
				b_leg_id TEXT NOT NULL DEFAULT '',
				provider_charge_id TEXT NOT NULL DEFAULT '',
				head_key TEXT NOT NULL DEFAULT '',
				subject_kind TEXT NOT NULL DEFAULT '',
				subject_json TEXT NOT NULL DEFAULT '',
				owner TEXT NOT NULL CHECK (owner IN ('v1', 'v2')),
				marker_version INTEGER NOT NULL CHECK (marker_version > 0),
				marker_epoch INTEGER NOT NULL CHECK (marker_epoch > 0),
				marker_generation INTEGER NOT NULL CHECK (marker_generation IN (1, 2)),
				marker_state TEXT NOT NULL CHECK (marker_state IN ('v1_active', 'v2_shadow', 'v1_draining', 'v2_active')),
				status TEXT NOT NULL CHECK (status IN ('pinned', 'completed')),
				completion_operation_key TEXT NOT NULL DEFAULT '',
				completion_transaction_id TEXT NOT NULL DEFAULT '',
				created_at_unix INTEGER NOT NULL CHECK (created_at_unix > 0),
				updated_at_unix INTEGER NOT NULL CHECK (updated_at_unix > 0 AND updated_at_unix >= created_at_unix),
				completed_at_unix INTEGER NOT NULL DEFAULT 0 CHECK (completed_at_unix >= 0),
				UNIQUE(store_id, operation_kind, operation_key)
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS ` + billingPostingOwnershipPinIndex + `
				ON billing_posting_ownership_pins(store_id, operation_kind, operation_key)`,
			`CREATE INDEX IF NOT EXISTS ` + billingPostingOwnershipAccountIndex + `
				ON billing_posting_ownership_pins(store_id, account_id, call_id, operation_kind)`,
			`CREATE INDEX IF NOT EXISTS ` + billingPostingOwnershipStatusIndex + `
				ON billing_posting_ownership_pins(store_id, operation_kind, status)`,
		}
	case dialect.PG:
		statements = []string{
			`CREATE TABLE IF NOT EXISTS billing_posting_ownership_pins (
				id BIGSERIAL PRIMARY KEY,
				store_id TEXT NOT NULL,
				operation_kind TEXT NOT NULL CHECK (operation_kind IN ('customer_call_settlement', 'provider_charge', 'financial_adjustment')),
				operation_key TEXT NOT NULL CHECK (char_length(operation_key) > 0 AND char_length(operation_key) <= 1024),
				account_id TEXT NOT NULL CHECK (char_length(account_id) > 0 AND char_length(account_id) <= 512),
				call_id TEXT NOT NULL DEFAULT '' CHECK (char_length(call_id) <= 128),
				b_leg_id TEXT NOT NULL DEFAULT '',
				provider_charge_id TEXT NOT NULL DEFAULT '',
				head_key TEXT NOT NULL DEFAULT '',
				subject_kind TEXT NOT NULL DEFAULT '',
				subject_json TEXT NOT NULL DEFAULT '',
				owner TEXT NOT NULL CHECK (owner IN ('v1', 'v2')),
				marker_version BIGINT NOT NULL CHECK (marker_version > 0),
				marker_epoch BIGINT NOT NULL CHECK (marker_epoch > 0),
				marker_generation INTEGER NOT NULL CHECK (marker_generation IN (1, 2)),
				marker_state TEXT NOT NULL CHECK (marker_state IN ('v1_active', 'v2_shadow', 'v1_draining', 'v2_active')),
				status TEXT NOT NULL CHECK (status IN ('pinned', 'completed')),
				completion_operation_key TEXT NOT NULL DEFAULT '',
				completion_transaction_id TEXT NOT NULL DEFAULT '',
				created_at_unix BIGINT NOT NULL CHECK (created_at_unix > 0),
				updated_at_unix BIGINT NOT NULL CHECK (updated_at_unix > 0 AND updated_at_unix >= created_at_unix),
				completed_at_unix BIGINT NOT NULL DEFAULT 0 CHECK (completed_at_unix >= 0),
				UNIQUE(store_id, operation_kind, operation_key)
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS ` + billingPostingOwnershipPinIndex + `
				ON billing_posting_ownership_pins(store_id, operation_kind, operation_key)`,
			`CREATE INDEX IF NOT EXISTS ` + billingPostingOwnershipAccountIndex + `
				ON billing_posting_ownership_pins(store_id, account_id, call_id, operation_kind)`,
			`CREATE INDEX IF NOT EXISTS ` + billingPostingOwnershipStatusIndex + `
				ON billing_posting_ownership_pins(store_id, operation_kind, status)`,
		}
	default:
		return fmt.Errorf("billing posting ownership schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing posting ownership DDL: %w", err)
		}
	}
	return nil
}
