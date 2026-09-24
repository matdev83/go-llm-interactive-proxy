package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// BillingEconomicPostingIntentMigrationName records explicit monetary posting
// intent for economic revision queue delivery state (Task 17.3 F2B).
//
// Only provider-queue provider-rating work with valid B-leg lineage can create
// provider payable/cost/exclusion through the production EconomicRevisionWorker
// with a provider-cost adapter. The new columns record that narrow intent per
// delivery row so drain classification, counts, and claim gates never guess
// from queue name alone and never block evidence-only customer rating,
// reconciliation jobs, shadow observations/valuations, or queues without a
// posting adapter:
//
//   - provider_posting INTEGER 0/1: 1 = monetary (drain counts/pins/gates),
//     0 = evidence-only (always operable).
//   - posting_owner TEXT: "v1"/"v2" for monetary (stable owner, renewable epoch
//     via pins), "" for evidence-only or legacy-inferred V1.
//
// Legacy rows backfill provider_posting=1 where queue='provider' and
// work_kind='provider_rating' (the pre-F2B monetary inference); all other
// rows remain evidence-only. New enqueues set both columns explicitly at
// ensure time; the immutable work payload is unchanged for compatibility.
const BillingEconomicPostingIntentMigrationName = "20261004000000"

const billingEconomicPostingIntentIndex = "idx_billing_economic_state_posting"

func registerBillingEconomicPostingIntentMigration() {
	migrations.MustRegister(billingEconomicPostingIntentSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func billingEconomicPostingIntentSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing economic posting intent schema: nil database")
	}
	if ctx == nil {
		return fmt.Errorf("billing economic posting intent schema: nil context")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite:
		stmts := []string{
			`ALTER TABLE billing_economic_revision_work_state ADD COLUMN provider_posting INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE billing_economic_revision_work_state ADD COLUMN posting_owner TEXT NOT NULL DEFAULT ''`,
		}
		for _, stmt := range stmts {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				// Idempotent: column may already exist after a retried migration.
				if !isDuplicateColumnError(err) {
					return fmt.Errorf("billing economic posting intent SQLite DDL: %w", err)
				}
			}
		}
		// Backfill legacy monetary inference: provider queue + provider_rating.
		if _, err := db.ExecContext(ctx, `UPDATE billing_economic_revision_work_state SET provider_posting = 1, posting_owner = 'v1' WHERE queue = 'provider' AND work_kind = 'provider_rating' AND provider_posting = 0`); err != nil {
			return fmt.Errorf("billing economic posting intent SQLite backfill: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS `+billingEconomicPostingIntentIndex+` ON billing_economic_revision_work_state(store_id, provider_posting, status)`); err != nil {
			return fmt.Errorf("billing economic posting intent SQLite index: %w", err)
		}
		return nil
	case dialect.PG:
		stmts := []string{
			`ALTER TABLE billing_economic_revision_work_state ADD COLUMN IF NOT EXISTS provider_posting INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE billing_economic_revision_work_state ADD COLUMN IF NOT EXISTS posting_owner TEXT NOT NULL DEFAULT ''`,
		}
		for _, stmt := range stmts {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("billing economic posting intent PostgreSQL DDL: %w", err)
			}
		}
		if _, err := db.ExecContext(ctx, `UPDATE billing_economic_revision_work_state SET provider_posting = 1, posting_owner = 'v1' WHERE queue = 'provider' AND work_kind = 'provider_rating' AND provider_posting = 0`); err != nil {
			return fmt.Errorf("billing economic posting intent PostgreSQL backfill: %w", err)
		}
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS `+billingEconomicPostingIntentIndex+` ON billing_economic_revision_work_state(store_id, provider_posting, status)`); err != nil {
			return fmt.Errorf("billing economic posting intent PostgreSQL index: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("billing economic posting intent schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}

func isDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// modernc.org/sqlite reports "duplicate column name" for repeated ADD COLUMN.
	for _, sub := range []string{"duplicate column", "already exists"} {
		if len(msg) >= len(sub) {
			for i := 0; i+len(sub) <= len(msg); i++ {
				if msg[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
