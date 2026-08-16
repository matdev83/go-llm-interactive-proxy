package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const ExposureMigrationName = "20260820000000"

const exposureAccountStatusIndex = "idx_billing_exposures_account_status"

func registerExposureMigration() {
	migrations.MustRegister(exposureSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func exposureSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing exposure schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = sqliteExposureDDL()
	case dialect.PG:
		statements = postgresExposureDDL()
	default:
		return fmt.Errorf("billing exposure schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "already exists") || strings.Contains(lower, "duplicate") {
				continue
			}
			return fmt.Errorf("billing exposure DDL: %w", err)
		}
	}
	return nil
}

func sqliteExposureDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS call_exposures (
			exposure_key TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			call_id TEXT NOT NULL,
			max_exposure_nano INTEGER NOT NULL,
			currency TEXT NOT NULL,
			pricing_ref TEXT NOT NULL,
			charge_policy_ref TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			balance_nano INTEGER NOT NULL,
			credit_floor_nano INTEGER NOT NULL,
			open_exposure_nano INTEGER NOT NULL,
			settled_headroom_nano INTEGER NOT NULL,
			safety_margin_before_nano INTEGER NOT NULL,
			safety_margin_after_nano INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'open',
			created_at TEXT NOT NULL,
			closed_at TEXT,
			UNIQUE(account_id, call_id),
			FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
		)`,
		`CREATE INDEX IF NOT EXISTS ` + exposureAccountStatusIndex + ` ON call_exposures(account_id, status, created_at)`,
		`CREATE TRIGGER IF NOT EXISTS billing_exposure_immutable_update BEFORE UPDATE ON call_exposures
			WHEN NEW.exposure_key IS NOT OLD.exposure_key
			 OR NEW.account_id IS NOT OLD.account_id
			 OR NEW.call_id IS NOT OLD.call_id
			 OR NEW.max_exposure_nano IS NOT OLD.max_exposure_nano
			 OR NEW.currency IS NOT OLD.currency
			 OR NEW.pricing_ref IS NOT OLD.pricing_ref
			 OR NEW.charge_policy_ref IS NOT OLD.charge_policy_ref
			 OR NEW.fingerprint IS NOT OLD.fingerprint
			 OR NEW.balance_nano IS NOT OLD.balance_nano
			 OR NEW.credit_floor_nano IS NOT OLD.credit_floor_nano
			 OR NEW.open_exposure_nano IS NOT OLD.open_exposure_nano
			 OR NEW.settled_headroom_nano IS NOT OLD.settled_headroom_nano
			 OR NEW.safety_margin_before_nano IS NOT OLD.safety_margin_before_nano
			 OR NEW.safety_margin_after_nano IS NOT OLD.safety_margin_after_nano
			 OR NEW.created_at IS NOT OLD.created_at
			BEGIN SELECT RAISE(ABORT, 'call exposures are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_exposure_immutable_delete BEFORE DELETE ON call_exposures BEGIN SELECT RAISE(ABORT, 'call exposures are immutable'); END`,
	}
}

func postgresExposureDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS call_exposures (
			exposure_key TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			call_id TEXT NOT NULL,
			max_exposure_nano BIGINT NOT NULL,
			currency TEXT NOT NULL,
			pricing_ref TEXT NOT NULL,
			charge_policy_ref TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			balance_nano BIGINT NOT NULL,
			credit_floor_nano BIGINT NOT NULL,
			open_exposure_nano BIGINT NOT NULL,
			settled_headroom_nano BIGINT NOT NULL,
			safety_margin_before_nano BIGINT NOT NULL,
			safety_margin_after_nano BIGINT NOT NULL,
			status TEXT NOT NULL DEFAULT 'open',
			created_at TEXT NOT NULL,
			closed_at TEXT,
			UNIQUE(account_id, call_id),
			FOREIGN KEY(account_id) REFERENCES billing_accounts(account_id)
		)`,
		`CREATE INDEX IF NOT EXISTS ` + exposureAccountStatusIndex + ` ON call_exposures(account_id, status, created_at)`,
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
