package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingEconomicRevisionHeadsMigrationName identifies the additive durable
// current-head projection and mutable queue state for revision-keyed pure
// economics.
const BillingEconomicRevisionHeadsMigrationName = "20260920000000"

const (
	billingEconomicRevisionHeadIndex      = "idx_billing_economic_revision_heads_scope"
	billingEconomicRevisionWorkStateIndex = "idx_billing_economic_revision_work_state_due"
)

func registerBillingEconomicRevisionHeadsMigration() {
	migrations.MustRegister(billingEconomicRevisionHeadsSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingEconomicRevisionHeadsSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing economic revision heads schema: nil database")
	}
	if ctx == nil {
		return fmt.Errorf("billing economic revision heads schema: nil context")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = []string{
			`CREATE TABLE IF NOT EXISTS billing_economic_valuation_heads (
				id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
				store_id TEXT NOT NULL,
				queue TEXT NOT NULL,
				head_key TEXT NOT NULL,
				subject_kind TEXT NOT NULL,
				subject_id TEXT NOT NULL,
				subject_json TEXT NOT NULL,
				evidence_revision INTEGER NOT NULL CHECK (evidence_revision > 0),
				input_set_hash TEXT NOT NULL,
				work_id TEXT NOT NULL,
				valuation_id TEXT NOT NULL,
				valuation_version INTEGER NOT NULL CHECK (valuation_version > 0),
				reconciliation_id TEXT NOT NULL DEFAULT '',
				reconciliation_version INTEGER NOT NULL DEFAULT 0,
				valuation_fingerprint TEXT NOT NULL,
				reconciliation_fingerprint TEXT NOT NULL DEFAULT '',
				fingerprint TEXT NOT NULL,
				head_version INTEGER NOT NULL CHECK (head_version > 0),
				fence INTEGER NOT NULL CHECK (fence > 0),
				created_at_unix INTEGER NOT NULL,
				updated_at_unix INTEGER NOT NULL,
				UNIQUE(store_id, queue, head_key)
			)`,
			`CREATE INDEX IF NOT EXISTS ` + billingEconomicRevisionHeadIndex + `
					ON billing_economic_valuation_heads(store_id, queue, subject_kind, subject_id, evidence_revision, head_key)`,
			`CREATE TABLE IF NOT EXISTS billing_economic_revision_work_state (
				id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
				store_id TEXT NOT NULL,
				work_id TEXT NOT NULL,
				work_version INTEGER NOT NULL,
				queue TEXT NOT NULL,
				head_key TEXT NOT NULL,
				status TEXT NOT NULL CHECK (status IN ('pending', 'processing', 'completed')),
				attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
				next_attempt_at_unix INTEGER NOT NULL DEFAULT 0,
				lease_owner TEXT NOT NULL DEFAULT '',
				lease_until_unix INTEGER NOT NULL DEFAULT 0,
				last_error TEXT NOT NULL DEFAULT '',
				fence INTEGER NOT NULL DEFAULT 0 CHECK (fence >= 0),
				completed_at_unix INTEGER NOT NULL DEFAULT 0,
				created_at_unix INTEGER NOT NULL,
				updated_at_unix INTEGER NOT NULL,
				UNIQUE(store_id, work_id, work_version)
			)`,
			`CREATE INDEX IF NOT EXISTS ` + billingEconomicRevisionWorkStateIndex + `
					ON billing_economic_revision_work_state(store_id, queue, status, next_attempt_at_unix, lease_until_unix, created_at_unix, work_id, work_version)`,
		}
	case dialect.PG:
		statements = []string{
			`CREATE TABLE IF NOT EXISTS billing_economic_valuation_heads (
				id BIGSERIAL PRIMARY KEY,
				store_id TEXT NOT NULL,
				queue TEXT NOT NULL,
				head_key TEXT NOT NULL,
				subject_kind TEXT NOT NULL,
				subject_id TEXT NOT NULL,
				subject_json TEXT NOT NULL,
				evidence_revision BIGINT NOT NULL CHECK (evidence_revision > 0),
				input_set_hash TEXT NOT NULL,
				work_id TEXT NOT NULL,
				valuation_id TEXT NOT NULL,
				valuation_version BIGINT NOT NULL CHECK (valuation_version > 0),
				reconciliation_id TEXT NOT NULL DEFAULT '',
				reconciliation_version BIGINT NOT NULL DEFAULT 0,
				valuation_fingerprint TEXT NOT NULL,
				reconciliation_fingerprint TEXT NOT NULL DEFAULT '',
				fingerprint TEXT NOT NULL,
				head_version BIGINT NOT NULL CHECK (head_version > 0),
				fence BIGINT NOT NULL CHECK (fence > 0),
				created_at_unix BIGINT NOT NULL,
				updated_at_unix BIGINT NOT NULL,
				UNIQUE(store_id, queue, head_key)
			)`,
			`CREATE INDEX IF NOT EXISTS ` + billingEconomicRevisionHeadIndex + `
					ON billing_economic_valuation_heads(store_id, queue, subject_kind, subject_id, evidence_revision, head_key)`,
			`CREATE TABLE IF NOT EXISTS billing_economic_revision_work_state (
				id BIGSERIAL PRIMARY KEY,
				store_id TEXT NOT NULL,
				work_id TEXT NOT NULL,
				work_version BIGINT NOT NULL,
				queue TEXT NOT NULL,
				head_key TEXT NOT NULL,
				status TEXT NOT NULL CHECK (status IN ('pending', 'processing', 'completed')),
				attempt_count BIGINT NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
				next_attempt_at_unix BIGINT NOT NULL DEFAULT 0,
				lease_owner TEXT NOT NULL DEFAULT '',
				lease_until_unix BIGINT NOT NULL DEFAULT 0,
				last_error TEXT NOT NULL DEFAULT '',
				fence BIGINT NOT NULL DEFAULT 0 CHECK (fence >= 0),
				completed_at_unix BIGINT NOT NULL DEFAULT 0,
				created_at_unix BIGINT NOT NULL,
				updated_at_unix BIGINT NOT NULL,
				UNIQUE(store_id, work_id, work_version)
			)`,
			`CREATE INDEX IF NOT EXISTS ` + billingEconomicRevisionWorkStateIndex + `
					ON billing_economic_revision_work_state(store_id, queue, status, next_attempt_at_unix, lease_until_unix, created_at_unix, work_id, work_version)`,
		}
	default:
		return fmt.Errorf("billing economic revision heads schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing economic revision heads DDL: %w", err)
		}
	}
	return nil
}
