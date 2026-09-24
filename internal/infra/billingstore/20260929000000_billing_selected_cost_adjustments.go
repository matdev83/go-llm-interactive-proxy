package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingSelectedCostAdjustmentsMigrationName identifies the additive Task
// 13.3B selected-cost adjustment schema. The existing provider-cost head gains
// the exact selected valuation identity (valuation revision, selection plane,
// exact posted/native amounts, frozen FX basis and posting state), and the new
// immutable adjustment table retains the operation plus valuation-link
// identity.
const BillingSelectedCostAdjustmentsMigrationName = "20260929000000"

const (
	billingSelectedCostAdjustmentOperationIndex = "idx_billing_selected_cost_adjustments_operation"
	billingSelectedCostAdjustmentLinkIndex      = "idx_billing_selected_cost_adjustments_link"
	billingSelectedCostAdjustmentHeadIndex      = "idx_billing_selected_cost_adjustments_head"
)

func registerBillingSelectedCostAdjustmentsMigration() {
	migrations.MustRegister(billingSelectedCostAdjustmentsSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingSelectedCostAdjustmentsSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing selected cost adjustments schema: nil database")
	}
	if ctx == nil {
		return fmt.Errorf("billing selected cost adjustments schema: nil context")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = sqliteBillingSelectedCostAdjustmentsDDL()
	case dialect.PG:
		statements = postgresBillingSelectedCostAdjustmentsDDL()
	default:
		return fmt.Errorf("billing selected cost adjustments schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			// SQLite has no ADD COLUMN IF NOT EXISTS. A partially applied
			// migration must remain safely retryable after a process
			// interruption.
			lower := strings.ToLower(err.Error())
			if db.Dialect().Name() == dialect.SQLite && (strings.Contains(lower, "duplicate column") || strings.Contains(lower, "already exists")) {
				continue
			}
			return fmt.Errorf("billing selected cost adjustments DDL: %w", err)
		}
	}
	return nil
}

func sqliteBillingSelectedCostAdjustmentsDDL() []string {
	return []string{
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN valuation_revision INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN selection_status TEXT NOT NULL DEFAULT 'final'`,
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN selection_reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN selection_basis TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN selection_provenance TEXT NOT NULL DEFAULT 'attempted'`,
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN posted_amount_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN native_amount_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN fx_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN posting_state TEXT NOT NULL DEFAULT 'applied'`,
		`CREATE TABLE IF NOT EXISTS billing_selected_cost_adjustments (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			call_id TEXT NOT NULL,
			head_key TEXT NOT NULL,
			operation_key TEXT NOT NULL,
			link_key TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			status TEXT NOT NULL,
			comparison TEXT NOT NULL DEFAULT '',
			posting TEXT NOT NULL,
			previous_valuation_id TEXT NOT NULL DEFAULT '',
			previous_revision INTEGER NOT NULL DEFAULT 0,
			previous_input_set_hash TEXT NOT NULL DEFAULT '',
			current_valuation_id TEXT NOT NULL,
			current_revision INTEGER NOT NULL,
			current_input_set_hash TEXT NOT NULL,
			currency TEXT NOT NULL,
			fx_json TEXT NOT NULL DEFAULT '',
			adjustment_revision INTEGER NOT NULL,
			delta_json TEXT NOT NULL,
			journal_transaction_id TEXT NOT NULL DEFAULT '',
			created_at_unix INTEGER NOT NULL,
			UNIQUE(store_id, operation_key),
			UNIQUE(store_id, link_key),
			FOREIGN KEY(account_id) REFERENCES billing_accounts
		)`,
		`CREATE INDEX IF NOT EXISTS ` + billingSelectedCostAdjustmentHeadIndex + `
			ON billing_selected_cost_adjustments(store_id, account_id, call_id, head_key, adjustment_revision, id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + billingSelectedCostAdjustmentOperationIndex + `
			ON billing_selected_cost_adjustments(store_id, operation_key)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + billingSelectedCostAdjustmentLinkIndex + `
			ON billing_selected_cost_adjustments(store_id, link_key)`,
		`CREATE TRIGGER IF NOT EXISTS billing_selected_cost_adjustments_immutable_update BEFORE UPDATE ON billing_selected_cost_adjustments
			BEGIN SELECT RAISE(ABORT, 'billing selected cost adjustment is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_selected_cost_adjustments_immutable_delete BEFORE DELETE ON billing_selected_cost_adjustments
			BEGIN SELECT RAISE(ABORT, 'billing selected cost adjustment is immutable'); END`,
	}
}

func postgresBillingSelectedCostAdjustmentsDDL() []string {
	return []string{
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN IF NOT EXISTS valuation_revision BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN IF NOT EXISTS selection_status TEXT NOT NULL DEFAULT 'final'`,
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN IF NOT EXISTS selection_reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN IF NOT EXISTS selection_basis TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN IF NOT EXISTS selection_provenance TEXT NOT NULL DEFAULT 'attempted'`,
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN IF NOT EXISTS posted_amount_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN IF NOT EXISTS native_amount_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN IF NOT EXISTS fx_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE billing_provider_cost_heads ADD COLUMN IF NOT EXISTS posting_state TEXT NOT NULL DEFAULT 'applied'`,
		`CREATE TABLE IF NOT EXISTS billing_selected_cost_adjustments (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			call_id TEXT NOT NULL,
			head_key TEXT NOT NULL,
			operation_key TEXT NOT NULL,
			link_key TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			status TEXT NOT NULL,
			comparison TEXT NOT NULL DEFAULT '',
			posting TEXT NOT NULL,
			previous_valuation_id TEXT NOT NULL DEFAULT '',
			previous_revision BIGINT NOT NULL DEFAULT 0,
			previous_input_set_hash TEXT NOT NULL DEFAULT '',
			current_valuation_id TEXT NOT NULL,
			current_revision BIGINT NOT NULL,
			current_input_set_hash TEXT NOT NULL,
			currency TEXT NOT NULL,
			fx_json TEXT NOT NULL DEFAULT '',
			adjustment_revision BIGINT NOT NULL,
			delta_json TEXT NOT NULL,
			journal_transaction_id TEXT NOT NULL DEFAULT '',
			created_at_unix BIGINT NOT NULL,
			UNIQUE(store_id, operation_key),
			UNIQUE(store_id, link_key),
			FOREIGN KEY(account_id) REFERENCES billing_accounts
		)`,
		`CREATE INDEX IF NOT EXISTS ` + billingSelectedCostAdjustmentHeadIndex + `
			ON billing_selected_cost_adjustments(store_id, account_id, call_id, head_key, adjustment_revision, id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + billingSelectedCostAdjustmentOperationIndex + `
			ON billing_selected_cost_adjustments(store_id, operation_key)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + billingSelectedCostAdjustmentLinkIndex + `
			ON billing_selected_cost_adjustments(store_id, link_key)`,
		`CREATE OR REPLACE FUNCTION billing_reject_selected_cost_adjustment_mutation() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'billing selected cost adjustment is immutable'; END; $$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS billing_selected_cost_adjustments_immutable ON billing_selected_cost_adjustments`,
		`CREATE TRIGGER billing_selected_cost_adjustments_immutable BEFORE UPDATE OR DELETE ON billing_selected_cost_adjustments FOR EACH ROW EXECUTE FUNCTION billing_reject_selected_cost_adjustment_mutation()`,
	}
}
