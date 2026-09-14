package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingV2ValuationIdentityMigrationName identifies the additive repair that
// persists the trusted economic context used by valuation identity. Existing
// rows retain an empty context hash and remain readable.
const BillingV2ValuationIdentityMigrationName = "20260914000000"

func registerBillingV2ValuationIdentityMigration() {
	migrations.MustRegister(billingV2ValuationIdentitySchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingV2ValuationIdentitySchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing V2 valuation identity schema: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite:
		var count int
		if err := db.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('billing_valuations') WHERE name = 'valuation_context_hash'`).Scan(ctx, &count); err != nil {
			return fmt.Errorf("billing V2 valuation identity SQLite column probe: %w", err)
		}
		if count == 0 {
			if _, err := db.ExecContext(ctx, `ALTER TABLE billing_valuations ADD COLUMN valuation_context_hash TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("billing V2 valuation identity SQLite column: %w", err)
			}
		}
	case dialect.PG:
		if _, err := db.ExecContext(ctx, `ALTER TABLE IF EXISTS billing_valuations ADD COLUMN IF NOT EXISTS valuation_context_hash TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("billing V2 valuation identity PostgreSQL column: %w", err)
		}
	default:
		return fmt.Errorf("billing V2 valuation identity schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	statements := []string{
		`DROP INDEX IF EXISTS idx_billing_valuations_input_identity`,
	}
	if db.Dialect().Name() == dialect.SQLite {
		statements = append(statements, `CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_valuations_input_identity
			ON billing_valuations(store_id, input_set_hash, valuation_context_hash, rater_id, rater_version, policy_id, policy_version, tariff_id, tariff_version, basis)
			WHERE input_set_hash != ''`,
			`DROP TRIGGER IF EXISTS billing_v2_valuations_immutable_update`,
			`CREATE TRIGGER billing_v2_valuations_immutable_update BEFORE UPDATE ON billing_valuations
				WHEN NEW.store_id IS NOT OLD.store_id OR NEW.valuation_id IS NOT OLD.valuation_id OR NEW.valuation_version IS NOT OLD.valuation_version
				 OR NEW.valuation_context_hash IS NOT OLD.valuation_context_hash
				 OR NEW.canonical_json IS NOT OLD.canonical_json OR NEW.fingerprint IS NOT OLD.fingerprint OR NEW.created_at_unix IS NOT OLD.created_at_unix
				BEGIN SELECT RAISE(ABORT, 'billing valuation is immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS billing_v2_valuations_immutable_delete BEFORE DELETE ON billing_valuations BEGIN SELECT RAISE(ABORT, 'billing valuation is immutable'); END`)
	} else {
		statements = append(statements, `CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_valuations_input_identity
			ON billing_valuations(store_id, input_set_hash, valuation_context_hash, rater_id, rater_version, policy_id, policy_version, tariff_id, tariff_version, basis)
			WHERE input_set_hash <> ''`,
			`CREATE OR REPLACE FUNCTION billing_reject_v2_valuation_mutation() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'DELETE' OR NEW.store_id IS DISTINCT FROM OLD.store_id OR NEW.valuation_id IS DISTINCT FROM OLD.valuation_id
     OR NEW.valuation_version IS DISTINCT FROM OLD.valuation_version OR NEW.valuation_context_hash IS DISTINCT FROM OLD.valuation_context_hash
     OR NEW.canonical_json IS DISTINCT FROM OLD.canonical_json OR NEW.fingerprint IS DISTINCT FROM OLD.fingerprint OR NEW.created_at_unix IS DISTINCT FROM OLD.created_at_unix THEN
    RAISE EXCEPTION 'billing valuation is immutable';
  END IF;
  RETURN NEW;
END; $$ LANGUAGE plpgsql`,
			`DROP TRIGGER IF EXISTS billing_v2_valuations_immutable ON billing_valuations`,
			`CREATE TRIGGER billing_v2_valuations_immutable BEFORE UPDATE OR DELETE ON billing_valuations FOR EACH ROW EXECUTE FUNCTION billing_reject_v2_valuation_mutation()`)
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing V2 valuation identity DDL: %w", err)
		}
	}
	return nil
}
