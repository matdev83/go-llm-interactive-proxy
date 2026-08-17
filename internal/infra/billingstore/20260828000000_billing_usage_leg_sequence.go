package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const UsageLegSequenceMigrationName = "20260828000000"

const usageLegCallAttemptSeqIndex = "idx_usage_leg_records_call_attempt_seq"

// registerUsageLegSequenceMigration adds nullable attempt_seq; legacy rows remain unknown and corrected positive sequences are unique per call.
func registerUsageLegSequenceMigration() {
	migrations.MustRegister(usageLegSequenceSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func usageLegSequenceSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing usage-leg sequence schema: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite:
		var columnCount int
		if err := db.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('usage_leg_records') WHERE name = ?`, "attempt_seq").Scan(ctx, &columnCount); err != nil {
			return fmt.Errorf("billing usage-leg sequence SQLite column probe: %w", err)
		}
		if columnCount == 0 {
			if _, err := db.ExecContext(ctx, `ALTER TABLE usage_leg_records ADD COLUMN attempt_seq INTEGER NULL`); err != nil {
				return fmt.Errorf("billing usage-leg sequence SQLite add column: %w", err)
			}
		}
		if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS `+usageLegCallAttemptSeqIndex+` ON usage_leg_records(call_id, attempt_seq)`); err != nil {
			return fmt.Errorf("billing usage-leg sequence SQLite index: %w", err)
		}
		return nil
	case dialect.PG:
		statements := []string{
			`ALTER TABLE usage_leg_records ADD COLUMN IF NOT EXISTS attempt_seq BIGINT NULL`,
			`CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ` + usageLegCallAttemptSeqIndex + ` ON usage_leg_records(call_id, attempt_seq)`,
		}
		for _, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("billing usage-leg sequence PostgreSQL DDL: %w", err)
			}
		}
		return nil
	default:
		return fmt.Errorf("billing usage-leg sequence schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}
