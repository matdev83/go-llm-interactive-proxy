package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// CostPassThroughHeadMigrationName identifies the mutable CAS-protected
// customer cost-pass-through head. The head is a rebuildable pointer to the
// immutable settlement/journal history, never a replacement for that history.
const CostPassThroughHeadMigrationName = "20260916000000"

const costPassThroughHeadCallIndex = "idx_billing_cost_pass_through_heads_call"

func registerCostPassThroughHeadMigration() {
	migrations.MustRegister(costPassThroughHeadSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func costPassThroughHeadSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing cost pass-through head schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = costPassThroughHeadSQLiteDDL()
	case dialect.PG:
		statements = costPassThroughHeadPostgresDDL()
	default:
		return fmt.Errorf("billing cost pass-through head schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing cost pass-through head DDL: %w", err)
		}
	}
	return nil
}

func costPassThroughHeadSQLiteDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS billing_cost_pass_through_heads (
			head_key TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			call_id TEXT NOT NULL,
			settlement_operation_key TEXT NOT NULL,
			original_transaction_id TEXT NOT NULL DEFAULT '',
			a_leg_id TEXT NOT NULL DEFAULT '',
			policy_id TEXT NOT NULL,
			policy_version TEXT NOT NULL,
			missing_cost TEXT NOT NULL CHECK (missing_cost IN ('pending','provisional')),
			safe_bound_nano INTEGER NOT NULL CHECK (safe_bound_nano >= 0),
			currency TEXT NOT NULL,
			allow_late_adjustment INTEGER NOT NULL CHECK (allow_late_adjustment IN (0,1)),
			status TEXT NOT NULL CHECK (status IN ('pending','provisional','final')),
			posted_amount_nano INTEGER NOT NULL CHECK (posted_amount_nano >= 0),
			provider_lur_key TEXT NOT NULL DEFAULT '',
			provider_valuation_id TEXT NOT NULL DEFAULT '',
			provider_revision INTEGER NOT NULL CHECK (provider_revision >= 0),
			provider_input_hash TEXT NOT NULL DEFAULT '',
			settlement_fingerprint TEXT NOT NULL,
			head_version INTEGER NOT NULL CHECK (head_version > 0),
			fence INTEGER NOT NULL CHECK (fence > 0),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(account_id, call_id),
			FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id),
			CHECK (posted_amount_nano <= safe_bound_nano),
			CHECK (status <> 'pending' OR posted_amount_nano = 0),
			CHECK (status <> 'provisional' OR allow_late_adjustment <> 0),
			CHECK ((provider_revision = 0 AND provider_lur_key = '' AND provider_valuation_id = '' AND provider_input_hash = '') OR provider_revision > 0)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + costPassThroughHeadCallIndex + ` ON billing_cost_pass_through_heads(account_id, call_id)`,
	}
}

func costPassThroughHeadPostgresDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS billing_cost_pass_through_heads (
			head_key TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			call_id TEXT NOT NULL,
			settlement_operation_key TEXT NOT NULL,
			original_transaction_id TEXT NOT NULL DEFAULT '',
			a_leg_id TEXT NOT NULL DEFAULT '',
			policy_id TEXT NOT NULL,
			policy_version TEXT NOT NULL,
			missing_cost TEXT NOT NULL CHECK (missing_cost IN ('pending','provisional')),
			safe_bound_nano BIGINT NOT NULL CHECK (safe_bound_nano >= 0),
			currency TEXT NOT NULL,
			allow_late_adjustment BOOLEAN NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending','provisional','final')),
			posted_amount_nano BIGINT NOT NULL CHECK (posted_amount_nano >= 0),
			provider_lur_key TEXT NOT NULL DEFAULT '',
			provider_valuation_id TEXT NOT NULL DEFAULT '',
			provider_revision BIGINT NOT NULL CHECK (provider_revision >= 0),
			provider_input_hash TEXT NOT NULL DEFAULT '',
			settlement_fingerprint TEXT NOT NULL,
			head_version BIGINT NOT NULL CHECK (head_version > 0),
			fence BIGINT NOT NULL CHECK (fence > 0),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(account_id, call_id),
			FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id),
			CHECK (posted_amount_nano <= safe_bound_nano),
			CHECK (status <> 'pending' OR posted_amount_nano = 0),
			CHECK (status <> 'provisional' OR allow_late_adjustment),
			CHECK ((provider_revision = 0 AND provider_lur_key = '' AND provider_valuation_id = '' AND provider_input_hash = '') OR provider_revision > 0)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + costPassThroughHeadCallIndex + ` ON billing_cost_pass_through_heads(account_id, call_id)`,
	}
}
