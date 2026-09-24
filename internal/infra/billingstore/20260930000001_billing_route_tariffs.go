package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// RouteTariffBindingMigrationName adds the frozen per-route customer tariff
// bindings to call_exposures. A rich quote freezes every evaluated route
// tariff (route key plus tariff version and content hash); terminal settlement
// compares the rated routes against this binding before posting.
const RouteTariffBindingMigrationName = "20260930000001"

// registerRouteTariffBindingMigration registers the additive exposure binding
// migration. Legacy rows keep an empty binding set, which preserves the
// base-chain settlement path.
func registerRouteTariffBindingMigration() {
	migrations.MustRegister(routeTariffBindingUp, func(context.Context, *bun.DB) error { return nil })
}

func routeTariffBindingUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing route tariff binding: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = sqliteRouteTariffBindingDDL()
	case dialect.PG:
		statements = postgresRouteTariffBindingDDL()
	default:
		return fmt.Errorf("billing route tariff binding: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "already exists") || strings.Contains(lower, "duplicate") {
				continue
			}
			return fmt.Errorf("billing route tariff binding DDL: %w", err)
		}
	}
	return nil
}

func sqliteRouteTariffBindingDDL() []string {
	return []string{
		`ALTER TABLE call_exposures ADD COLUMN route_tariffs TEXT NOT NULL DEFAULT '[]'`,
		`DROP TRIGGER IF EXISTS billing_exposure_immutable_update`,
		`CREATE TRIGGER billing_exposure_immutable_update BEFORE UPDATE ON call_exposures
			WHEN NEW.exposure_key IS NOT OLD.exposure_key
			 OR NEW.account_id IS NOT OLD.account_id
			 OR NEW.call_id IS NOT OLD.call_id
			 OR NEW.max_exposure_nano IS NOT OLD.max_exposure_nano
			 OR NEW.currency IS NOT OLD.currency
			 OR NEW.pricing_ref IS NOT OLD.pricing_ref
			 OR NEW.charge_policy_ref IS NOT OLD.charge_policy_ref
			 OR NEW.route_tariffs IS NOT OLD.route_tariffs
			 OR NEW.fingerprint IS NOT OLD.fingerprint
			 OR NEW.balance_nano IS NOT OLD.balance_nano
			 OR NEW.credit_floor_nano IS NOT OLD.credit_floor_nano
			 OR NEW.open_exposure_nano IS NOT OLD.open_exposure_nano
			 OR NEW.settled_headroom_nano IS NOT OLD.settled_headroom_nano
			 OR NEW.safety_margin_before_nano IS NOT OLD.safety_margin_before_nano
			 OR NEW.safety_margin_after_nano IS NOT OLD.safety_margin_after_nano
			 OR NEW.created_at IS NOT OLD.created_at
			BEGIN SELECT RAISE(ABORT, 'call exposures are immutable'); END`,
	}
}

func postgresRouteTariffBindingDDL() []string {
	return []string{
		`ALTER TABLE call_exposures ADD COLUMN IF NOT EXISTS route_tariffs TEXT NOT NULL DEFAULT '[]'`,
		`CREATE OR REPLACE FUNCTION billing_reject_exposure_mutation() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'call exposures are immutable';
  END IF;
  IF NEW.exposure_key IS DISTINCT FROM OLD.exposure_key
     OR NEW.account_id IS DISTINCT FROM OLD.account_id
     OR NEW.call_id IS DISTINCT FROM OLD.call_id
     OR NEW.max_exposure_nano IS DISTINCT FROM OLD.max_exposure_nano
     OR NEW.currency IS DISTINCT FROM OLD.currency
     OR NEW.pricing_ref IS DISTINCT FROM OLD.pricing_ref
     OR NEW.charge_policy_ref IS DISTINCT FROM OLD.charge_policy_ref
     OR NEW.route_tariffs IS DISTINCT FROM OLD.route_tariffs
     OR NEW.fingerprint IS DISTINCT FROM OLD.fingerprint
     OR NEW.balance_nano IS DISTINCT FROM OLD.balance_nano
     OR NEW.credit_floor_nano IS DISTINCT FROM OLD.credit_floor_nano
     OR NEW.open_exposure_nano IS DISTINCT FROM OLD.open_exposure_nano
     OR NEW.settled_headroom_nano IS DISTINCT FROM OLD.settled_headroom_nano
     OR NEW.safety_margin_before_nano IS DISTINCT FROM OLD.safety_margin_before_nano
     OR NEW.safety_margin_after_nano IS DISTINCT FROM OLD.safety_margin_after_nano
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'call exposures are immutable';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS billing_exposure_immutable ON call_exposures`,
		`CREATE TRIGGER billing_exposure_immutable BEFORE UPDATE OR DELETE ON call_exposures FOR EACH ROW EXECUTE FUNCTION billing_reject_exposure_mutation()`,
	}
}
