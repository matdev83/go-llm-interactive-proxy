package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingV2EconomicsMigrationName identifies the additive durable V2
// valuation/reconciliation projections.
const BillingV2EconomicsMigrationName = "20260912010000"

// EconomicsV2MigrationName is a descriptive alias used by composition code.
const EconomicsV2MigrationName = BillingV2EconomicsMigrationName

const BillingEconomicsProjectionVersion = 1

const (
	billingValuationInputIndex      = "idx_billing_valuations_input_identity"
	billingValuationSubjectIndex    = "idx_billing_valuations_store_subject"
	billingValuationLineItemIndex   = "idx_billing_valuation_lines_store_item"
	billingReconciliationSubjectIdx = "idx_billing_reconciliations_store_subject"
	billingReconciliationInputIndex = "idx_billing_reconciliations_store_basis_input"
	billingEconomicWorkPendingIndex = "idx_billing_economic_work_pending"
)

func registerBillingV2EconomicsMigration() {
	migrations.MustRegister(billingV2EconomicsSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingV2EconomicsSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing V2 economics schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = billingV2SQLiteDDL()
	case dialect.PG:
		statements = billingV2PostgresDDL()
	default:
		return fmt.Errorf("billing V2 economics schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	if db.Dialect().Name() == dialect.SQLite {
		var count int
		if err := db.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('billing_reconciliations') WHERE name = 'canonical_json'`).Scan(ctx, &count); err != nil {
			return fmt.Errorf("billing V2 economics SQLite canonical JSON probe: %w", err)
		}
		if count == 0 {
			// The table may have been created by an interrupted/older V2
			// deployment. Additive repair must happen before triggers refer to
			// the column below.
			var tableCount int
			if err := db.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'billing_reconciliations'`).Scan(ctx, &tableCount); err != nil {
				return fmt.Errorf("billing V2 economics SQLite reconciliation table probe: %w", err)
			}
			if tableCount > 0 {
				if _, err := db.ExecContext(ctx, `ALTER TABLE billing_reconciliations ADD COLUMN canonical_json TEXT NOT NULL DEFAULT ''`); err != nil {
					return fmt.Errorf("billing V2 economics SQLite canonical JSON column: %w", err)
				}
			}
		}
	} else if _, err := db.ExecContext(ctx, `ALTER TABLE IF EXISTS billing_reconciliations ADD COLUMN IF NOT EXISTS canonical_json TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("billing V2 economics PostgreSQL canonical JSON column: %w", err)
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing V2 economics DDL: %w", err)
		}
	}
	return nil
}

func billingV2SQLiteDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS billing_valuations (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			valuation_id TEXT NOT NULL,
			valuation_version INTEGER NOT NULL,
			perspective TEXT NOT NULL,
			basis TEXT NOT NULL,
			subject_kind TEXT NOT NULL,
			subject_id TEXT NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT '',
			scope TEXT NOT NULL DEFAULT '',
			input_set_hash TEXT NOT NULL DEFAULT '',
			rater_id TEXT NOT NULL DEFAULT '',
			rater_version TEXT NOT NULL DEFAULT '',
			tariff_id TEXT NOT NULL DEFAULT '',
			tariff_version TEXT NOT NULL DEFAULT '',
			policy_id TEXT NOT NULL DEFAULT '',
			policy_version TEXT NOT NULL DEFAULT '',
			qualifier_snapshot TEXT NOT NULL DEFAULT '',
			canonical_json TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			projection_version INTEGER NOT NULL DEFAULT 1,
			created_at_unix INTEGER NOT NULL,
			UNIQUE(store_id, valuation_id, valuation_version)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_valuations_input_identity
			ON billing_valuations(store_id, input_set_hash, rater_id, rater_version, policy_id, policy_version, tariff_id, tariff_version, basis)
			WHERE input_set_hash != ''`,
		`CREATE INDEX IF NOT EXISTS idx_billing_valuations_store_subject
			ON billing_valuations(store_id, subject_kind, subject_id, created_at_unix, valuation_id, valuation_version, id)`,
		`CREATE TABLE IF NOT EXISTS billing_valuation_lines (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			valuation_id TEXT NOT NULL,
			valuation_version INTEGER NOT NULL,
			line_id TEXT NOT NULL,
			rule_id TEXT NOT NULL,
			item_id TEXT NOT NULL,
			component_key TEXT NOT NULL DEFAULT '',
			component_key_hash TEXT NOT NULL DEFAULT '',
			fixed_fee_id TEXT NOT NULL DEFAULT '',
			fixed_fee_scope TEXT NOT NULL DEFAULT '',
			fixed_fee_version TEXT NOT NULL DEFAULT '',
			unit TEXT NOT NULL,
			quantity_coefficient TEXT NOT NULL DEFAULT '',
			quantity_scale INTEGER NOT NULL DEFAULT 0,
			quantity_present INTEGER NOT NULL DEFAULT 0,
			unit_price_coefficient TEXT NOT NULL DEFAULT '',
			unit_price_scale INTEGER NOT NULL DEFAULT 0,
			unit_price_present INTEGER NOT NULL DEFAULT 0,
			rate_numerator_coefficient TEXT NOT NULL DEFAULT '',
			rate_numerator_scale INTEGER NOT NULL DEFAULT 0,
			rate_numerator_present INTEGER NOT NULL DEFAULT 0,
			rate_denominator_coefficient TEXT NOT NULL DEFAULT '',
			rate_denominator_scale INTEGER NOT NULL DEFAULT 0,
			rate_denominator_present INTEGER NOT NULL DEFAULT 0,
			amount_coefficient TEXT NOT NULL DEFAULT '',
			amount_scale INTEGER NOT NULL DEFAULT 0,
			amount_present INTEGER NOT NULL DEFAULT 0,
			rounded_nano INTEGER NOT NULL DEFAULT 0,
			rounded_currency TEXT NOT NULL DEFAULT '',
			rounded_present INTEGER NOT NULL DEFAULT 0,
			rounding_scope TEXT NOT NULL DEFAULT '',
			rounding_policy TEXT NOT NULL DEFAULT '',
			included_unit INTEGER NOT NULL DEFAULT 0,
			source_observation_refs_json TEXT NOT NULL DEFAULT '[]',
			adjustment_refs_json TEXT NOT NULL DEFAULT '[]',
			projection_version INTEGER NOT NULL DEFAULT 1,
			UNIQUE(store_id, valuation_id, valuation_version, line_id),
			FOREIGN KEY(store_id, valuation_id, valuation_version) REFERENCES billing_valuations(store_id, valuation_id, valuation_version)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_valuation_lines_store_item
			ON billing_valuation_lines(store_id, item_id, valuation_id, valuation_version, line_id)`,
		`CREATE TABLE IF NOT EXISTS billing_reconciliations (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			reconciliation_id TEXT NOT NULL,
			reconciliation_version INTEGER NOT NULL,
			subject_kind TEXT NOT NULL,
			subject_id TEXT NOT NULL,
			subject_json TEXT NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT '',
			scope TEXT NOT NULL DEFAULT '',
			basis TEXT NOT NULL,
			input_set_hash TEXT NOT NULL DEFAULT '',
			local_input_hash TEXT NOT NULL DEFAULT '',
			provider_input_hash TEXT NOT NULL DEFAULT '',
			policy_id TEXT NOT NULL DEFAULT '',
			policy_version TEXT NOT NULL DEFAULT '',
			result_json TEXT NOT NULL,
			canonical_json TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			projection_version INTEGER NOT NULL DEFAULT 1,
			created_at_unix INTEGER NOT NULL,
			UNIQUE(store_id, reconciliation_id, reconciliation_version)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_reconciliations_store_subject
			ON billing_reconciliations(store_id, subject_kind, subject_id, created_at_unix, reconciliation_id, reconciliation_version, id)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_reconciliations_store_basis_input
			ON billing_reconciliations(store_id, basis, input_set_hash, created_at_unix, reconciliation_id, reconciliation_version, id)`,
		`CREATE TABLE IF NOT EXISTS billing_economic_work (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			work_id TEXT NOT NULL,
			work_version INTEGER NOT NULL,
			kind TEXT NOT NULL,
			subject_kind TEXT NOT NULL DEFAULT '',
			subject_id TEXT NOT NULL DEFAULT '',
			input_set_hash TEXT NOT NULL DEFAULT '',
			payload_json TEXT NOT NULL DEFAULT '{}',
			fingerprint TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			created_at_unix INTEGER NOT NULL,
			UNIQUE(store_id, work_id, work_version)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_economic_work_pending
			ON billing_economic_work(store_id, status, created_at_unix, work_id, work_version, id)`,
		`CREATE TRIGGER IF NOT EXISTS billing_v2_valuations_immutable_update BEFORE UPDATE ON billing_valuations
			WHEN NEW.store_id IS NOT OLD.store_id OR NEW.valuation_id IS NOT OLD.valuation_id OR NEW.valuation_version IS NOT OLD.valuation_version
			 OR NEW.canonical_json IS NOT OLD.canonical_json OR NEW.fingerprint IS NOT OLD.fingerprint OR NEW.created_at_unix IS NOT OLD.created_at_unix
			BEGIN SELECT RAISE(ABORT, 'billing valuation is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_v2_valuations_immutable_delete BEFORE DELETE ON billing_valuations BEGIN SELECT RAISE(ABORT, 'billing valuation is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_v2_reconciliations_immutable_update BEFORE UPDATE ON billing_reconciliations
			WHEN NEW.store_id IS NOT OLD.store_id OR NEW.reconciliation_id IS NOT OLD.reconciliation_id OR NEW.reconciliation_version IS NOT OLD.reconciliation_version
			 OR NEW.canonical_json IS NOT OLD.canonical_json OR NEW.fingerprint IS NOT OLD.fingerprint OR NEW.created_at_unix IS NOT OLD.created_at_unix
			BEGIN SELECT RAISE(ABORT, 'billing reconciliation is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_v2_reconciliations_immutable_delete BEFORE DELETE ON billing_reconciliations BEGIN SELECT RAISE(ABORT, 'billing reconciliation is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_v2_economic_work_immutable_update BEFORE UPDATE ON billing_economic_work BEGIN SELECT RAISE(ABORT, 'billing economic work is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_v2_economic_work_immutable_delete BEFORE DELETE ON billing_economic_work BEGIN SELECT RAISE(ABORT, 'billing economic work is immutable'); END`,
	}
}

func billingV2PostgresDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS billing_valuations (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL,
			valuation_id TEXT NOT NULL,
			valuation_version BIGINT NOT NULL,
			perspective TEXT NOT NULL,
			basis TEXT NOT NULL,
			subject_kind TEXT NOT NULL,
			subject_id TEXT NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT '',
			scope TEXT NOT NULL DEFAULT '',
			input_set_hash TEXT NOT NULL DEFAULT '',
			rater_id TEXT NOT NULL DEFAULT '',
			rater_version TEXT NOT NULL DEFAULT '',
			tariff_id TEXT NOT NULL DEFAULT '',
			tariff_version TEXT NOT NULL DEFAULT '',
			policy_id TEXT NOT NULL DEFAULT '',
			policy_version TEXT NOT NULL DEFAULT '',
			qualifier_snapshot TEXT NOT NULL DEFAULT '',
			canonical_json TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			projection_version INTEGER NOT NULL DEFAULT 1,
			created_at_unix BIGINT NOT NULL,
			UNIQUE(store_id, valuation_id, valuation_version)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_valuations_input_identity
			ON billing_valuations(store_id, input_set_hash, rater_id, rater_version, policy_id, policy_version, tariff_id, tariff_version, basis)
			WHERE input_set_hash <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_billing_valuations_store_subject
			ON billing_valuations(store_id, subject_kind, subject_id, created_at_unix, valuation_id, valuation_version, id)`,
		`CREATE TABLE IF NOT EXISTS billing_valuation_lines (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL,
			valuation_id TEXT NOT NULL,
			valuation_version BIGINT NOT NULL,
			line_id TEXT NOT NULL,
			rule_id TEXT NOT NULL,
			item_id TEXT NOT NULL,
			component_key TEXT NOT NULL DEFAULT '',
			component_key_hash TEXT NOT NULL DEFAULT '',
			fixed_fee_id TEXT NOT NULL DEFAULT '',
			fixed_fee_scope TEXT NOT NULL DEFAULT '',
			fixed_fee_version TEXT NOT NULL DEFAULT '',
			unit TEXT NOT NULL,
			quantity_coefficient TEXT NOT NULL DEFAULT '', quantity_scale INTEGER NOT NULL DEFAULT 0, quantity_present BOOLEAN NOT NULL DEFAULT FALSE,
			unit_price_coefficient TEXT NOT NULL DEFAULT '', unit_price_scale INTEGER NOT NULL DEFAULT 0, unit_price_present BOOLEAN NOT NULL DEFAULT FALSE,
			rate_numerator_coefficient TEXT NOT NULL DEFAULT '', rate_numerator_scale INTEGER NOT NULL DEFAULT 0, rate_numerator_present BOOLEAN NOT NULL DEFAULT FALSE,
			rate_denominator_coefficient TEXT NOT NULL DEFAULT '', rate_denominator_scale INTEGER NOT NULL DEFAULT 0, rate_denominator_present BOOLEAN NOT NULL DEFAULT FALSE,
			amount_coefficient TEXT NOT NULL DEFAULT '', amount_scale INTEGER NOT NULL DEFAULT 0, amount_present BOOLEAN NOT NULL DEFAULT FALSE,
			rounded_nano BIGINT NOT NULL DEFAULT 0, rounded_currency TEXT NOT NULL DEFAULT '', rounded_present BOOLEAN NOT NULL DEFAULT FALSE,
			rounding_scope TEXT NOT NULL DEFAULT '', rounding_policy TEXT NOT NULL DEFAULT '', included_unit BOOLEAN NOT NULL DEFAULT FALSE,
		source_observation_refs_json TEXT NOT NULL DEFAULT '[]', adjustment_refs_json TEXT NOT NULL DEFAULT '[]',
			projection_version INTEGER NOT NULL DEFAULT 1,
			UNIQUE(store_id, valuation_id, valuation_version, line_id),
			FOREIGN KEY(store_id, valuation_id, valuation_version) REFERENCES billing_valuations(store_id, valuation_id, valuation_version)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_valuation_lines_store_item ON billing_valuation_lines(store_id, item_id, valuation_id, valuation_version, line_id)`,
		`CREATE TABLE IF NOT EXISTS billing_reconciliations (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL,
			reconciliation_id TEXT NOT NULL,
			reconciliation_version BIGINT NOT NULL,
			subject_kind TEXT NOT NULL,
			subject_id TEXT NOT NULL,
			subject_json TEXT NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT '', scope TEXT NOT NULL DEFAULT '', basis TEXT NOT NULL,
			input_set_hash TEXT NOT NULL DEFAULT '', local_input_hash TEXT NOT NULL DEFAULT '', provider_input_hash TEXT NOT NULL DEFAULT '',
			policy_id TEXT NOT NULL DEFAULT '', policy_version TEXT NOT NULL DEFAULT '', result_json TEXT NOT NULL, canonical_json TEXT NOT NULL, fingerprint TEXT NOT NULL,
			projection_version INTEGER NOT NULL DEFAULT 1, created_at_unix BIGINT NOT NULL,
			UNIQUE(store_id, reconciliation_id, reconciliation_version)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_reconciliations_store_subject ON billing_reconciliations(store_id, subject_kind, subject_id, created_at_unix, reconciliation_id, reconciliation_version, id)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_reconciliations_store_basis_input ON billing_reconciliations(store_id, basis, input_set_hash, created_at_unix, reconciliation_id, reconciliation_version, id)`,
		`CREATE TABLE IF NOT EXISTS billing_economic_work (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL, work_id TEXT NOT NULL, work_version BIGINT NOT NULL, kind TEXT NOT NULL,
			subject_kind TEXT NOT NULL DEFAULT '', subject_id TEXT NOT NULL DEFAULT '', input_set_hash TEXT NOT NULL DEFAULT '',
			payload_json TEXT NOT NULL DEFAULT '{}', fingerprint TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', created_at_unix BIGINT NOT NULL,
			UNIQUE(store_id, work_id, work_version)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_economic_work_pending ON billing_economic_work(store_id, status, created_at_unix, work_id, work_version, id)`,
		`CREATE OR REPLACE FUNCTION billing_reject_v2_economic_mutation() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'billing V2 economic record is immutable'; END; $$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS billing_v2_economic_work_immutable ON billing_economic_work`,
		`CREATE TRIGGER billing_v2_economic_work_immutable BEFORE UPDATE OR DELETE ON billing_economic_work FOR EACH ROW EXECUTE FUNCTION billing_reject_v2_economic_mutation()`,
		`CREATE OR REPLACE FUNCTION billing_reject_v2_valuation_mutation() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'DELETE' OR NEW.store_id IS DISTINCT FROM OLD.store_id OR NEW.valuation_id IS DISTINCT FROM OLD.valuation_id
     OR NEW.valuation_version IS DISTINCT FROM OLD.valuation_version OR NEW.canonical_json IS DISTINCT FROM OLD.canonical_json
     OR NEW.fingerprint IS DISTINCT FROM OLD.fingerprint OR NEW.created_at_unix IS DISTINCT FROM OLD.created_at_unix THEN
    RAISE EXCEPTION 'billing valuation is immutable';
  END IF;
  RETURN NEW;
END; $$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS billing_v2_valuations_immutable ON billing_valuations`,
		`CREATE TRIGGER billing_v2_valuations_immutable BEFORE UPDATE OR DELETE ON billing_valuations FOR EACH ROW EXECUTE FUNCTION billing_reject_v2_valuation_mutation()`,
		`CREATE OR REPLACE FUNCTION billing_reject_v2_reconciliation_mutation() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'DELETE' OR NEW.store_id IS DISTINCT FROM OLD.store_id OR NEW.reconciliation_id IS DISTINCT FROM OLD.reconciliation_id
     OR NEW.reconciliation_version IS DISTINCT FROM OLD.reconciliation_version OR NEW.canonical_json IS DISTINCT FROM OLD.canonical_json
     OR NEW.fingerprint IS DISTINCT FROM OLD.fingerprint OR NEW.created_at_unix IS DISTINCT FROM OLD.created_at_unix THEN
    RAISE EXCEPTION 'billing reconciliation is immutable';
  END IF;
  RETURN NEW;
END; $$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS billing_v2_reconciliations_immutable ON billing_reconciliations`,
		`CREATE TRIGGER billing_v2_reconciliations_immutable BEFORE UPDATE OR DELETE ON billing_reconciliations FOR EACH ROW EXECUTE FUNCTION billing_reject_v2_reconciliation_mutation()`,
	}
}
