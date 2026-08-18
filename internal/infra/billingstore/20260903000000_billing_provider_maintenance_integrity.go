package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const ProviderMaintenanceIntegrityMigrationName = "20260903000000"

const providerMaintenanceFingerprintIndex = "idx_provider_maintenance_fingerprint"

func registerProviderMaintenanceIntegrityMigration() {
	migrations.MustRegister(providerMaintenanceIntegritySchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func providerMaintenanceIntegritySchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing provider-maintenance integrity schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS ` + providerMaintenanceFingerprintIndex + ` ON provider_maintenance_usage(fingerprint)`,
			`CREATE TRIGGER IF NOT EXISTS billing_provider_maintenance_immutable_update BEFORE UPDATE ON provider_maintenance_usage
				WHEN NEW.operation_id IS NOT OLD.operation_id
				 OR NEW.a_leg_id IS NOT OLD.a_leg_id
				 OR NEW.target_id IS NOT OLD.target_id
				 OR NEW.backend_id IS NOT OLD.backend_id
				 OR NEW.model_id IS NOT OLD.model_id
				 OR NEW.recorded_at IS NOT OLD.recorded_at
				 OR NEW.evidence_json IS NOT OLD.evidence_json
				 OR NEW.fingerprint IS NOT OLD.fingerprint
				BEGIN SELECT RAISE(ABORT, 'provider maintenance usage is immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS billing_provider_maintenance_immutable_delete BEFORE DELETE ON provider_maintenance_usage BEGIN SELECT RAISE(ABORT, 'provider maintenance usage is immutable'); END`,
		}
	case dialect.PG:
		statements = []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS ` + providerMaintenanceFingerprintIndex + ` ON provider_maintenance_usage(fingerprint)`,
			`CREATE OR REPLACE FUNCTION billing_reject_provider_maintenance_mutation() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'provider maintenance usage is immutable';
  END IF;
  IF NEW.operation_id IS DISTINCT FROM OLD.operation_id
     OR NEW.a_leg_id IS DISTINCT FROM OLD.a_leg_id
     OR NEW.target_id IS DISTINCT FROM OLD.target_id
     OR NEW.backend_id IS DISTINCT FROM OLD.backend_id
     OR NEW.model_id IS DISTINCT FROM OLD.model_id
     OR NEW.recorded_at IS DISTINCT FROM OLD.recorded_at
     OR NEW.evidence_json IS DISTINCT FROM OLD.evidence_json
     OR NEW.fingerprint IS DISTINCT FROM OLD.fingerprint THEN
    RAISE EXCEPTION 'provider maintenance usage is immutable';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`,
			`DROP TRIGGER IF EXISTS billing_provider_maintenance_immutable ON provider_maintenance_usage`,
			`CREATE TRIGGER billing_provider_maintenance_immutable BEFORE UPDATE OR DELETE ON provider_maintenance_usage FOR EACH ROW EXECUTE FUNCTION billing_reject_provider_maintenance_mutation()`,
		}
	default:
		return fmt.Errorf("billing provider-maintenance integrity schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "already exists") || strings.Contains(lower, "duplicate") {
				continue
			}
			return fmt.Errorf("billing provider-maintenance integrity DDL: %w", err)
		}
	}
	return nil
}
