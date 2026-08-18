package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const ProviderMaintenanceMigrationName = "20260902000000"

func registerProviderMaintenanceMigration() {
	migrations.MustRegister(providerMaintenanceSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func providerMaintenanceSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing provider-maintenance schema: nil database")
	}
	var ddl string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		ddl = `CREATE TABLE IF NOT EXISTS provider_maintenance_usage (
			operation_id TEXT PRIMARY KEY,
			a_leg_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			backend_id TEXT NOT NULL,
			model_id TEXT NOT NULL,
			recorded_at TEXT NOT NULL,
			evidence_json TEXT NOT NULL,
			fingerprint TEXT NOT NULL
		)`
	case dialect.PG:
		ddl = `CREATE TABLE IF NOT EXISTS provider_maintenance_usage (
			operation_id TEXT PRIMARY KEY,
			a_leg_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			backend_id TEXT NOT NULL,
			model_id TEXT NOT NULL,
			recorded_at TIMESTAMPTZ NOT NULL,
			evidence_json TEXT NOT NULL,
			fingerprint TEXT NOT NULL
		)`
	default:
		return fmt.Errorf("billing provider-maintenance schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("billing provider-maintenance DDL: %w", err)
	}
	return nil
}

var _ billing.ProviderMaintenanceUsageStore = (*DurableStore)(nil)

func marshalMaintenanceEvidence(usage billing.ProviderMaintenanceUsage) (string, error) {
	payload, err := json.Marshal(usage.Evidence)
	if err != nil {
		return "", fmt.Errorf("billingstore: encode provider maintenance evidence: %w", err)
	}
	return string(payload), nil
}

func (s *DurableStore) AppendProviderMaintenance(ctx context.Context, usage billing.ProviderMaintenanceUsage) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("billingstore: nil store")
	}
	if ctx == nil {
		return fmt.Errorf("billingstore: nil context")
	}
	if err := usage.Validate(); err != nil {
		return err
	}
	fingerprint, err := usage.Fingerprint()
	if err != nil {
		return err
	}
	payload, err := marshalMaintenanceEvidence(usage)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: begin provider maintenance: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var existing string
	err = tx.NewRaw(`SELECT fingerprint FROM provider_maintenance_usage WHERE operation_id = ?`, usage.OperationID).Scan(ctx, &existing)
	if err == nil {
		if existing != fingerprint {
			return fmt.Errorf("%w: provider maintenance operation %q", ErrIdentityConflict, usage.OperationID)
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("billingstore: lookup provider maintenance: %w", err)
	}
	if _, err := tx.NewRaw(`INSERT INTO provider_maintenance_usage(operation_id, a_leg_id, target_id, backend_id, model_id, recorded_at, evidence_json, fingerprint) VALUES (?,?,?,?,?,?,?,?)`, usage.OperationID, usage.ALegID, usage.TargetID, usage.BackendID, usage.ModelID, usage.RecordedAt, payload, fingerprint).Exec(ctx); err != nil {
		if isUniqueViolation(err) {
			_ = tx.Rollback()
			return s.AppendProviderMaintenance(ctx, usage)
		}
		return fmt.Errorf("billingstore: append provider maintenance: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: commit provider maintenance: %w", err)
	}
	return nil
}
