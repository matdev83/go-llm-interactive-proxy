package journalstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// LinkedStatementOrderedIndexMigrationName extends the linked-statement B-leg
// partial index with the ListObservations keyset columns. One index then
// serves both the store-scoped B-leg bound and the deterministic page
// ordering, so SQLite keeps the plan out of a temp B-tree. It replaces the
// interim store_id/b_leg_id-only index recorded by
// LinkedStatementIndexMigrationName. The canonical observation envelope
// remains the sole immutable source; this index is only a query accelerator.
const LinkedStatementOrderedIndexMigrationName = "20260919000000"

func registerLinkedStatementOrderedIndexMigration() {
	migrations.MustRegister(linkedStatementOrderedIndexUp, func(context.Context, *bun.DB) error { return nil })
}

func linkedStatementOrderedIndexUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("metering linked-statement ordered index schema: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite, dialect.PG:
		// DROP then CREATE converges both a fresh database and one that already
		// recorded the interim index shape. The migration stays unmarked on
		// failure, so a partial run retries the idempotent pair.
		stmts := []string{
			`DROP INDEX IF EXISTS idx_metering_facts_store_bleg`,
			`CREATE INDEX IF NOT EXISTS idx_metering_facts_store_bleg
				ON metering_facts(store_id, b_leg_id, stream_id, sequence, observation_id, observation_revision, id)
				WHERE payload_kind = 'observation'`,
		}
		for _, stmt := range stmts {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("metering linked-statement ordered index schema: %w", err)
			}
		}
		return nil
	default:
		return fmt.Errorf("metering linked-statement ordered index schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}
