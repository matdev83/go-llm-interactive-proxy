package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const UsageLegSequenceMigrationName = "20260828000000"

const usageLegCallAttemptSeqIndex = "idx_usage_leg_records_call_attempt_seq"

// registerUsageLegSequenceMigration adds nullable attempt_seq to
// usage_leg_records. Pre-fix rows keep NULL (order unknown); new corrected
// records persist the exact positive b2bua.BLegRecord.Seq. Known positive
// attempt sequences are unique per call; SQL NULL uniqueness semantics allow
// any number of legacy unknown-sequenc rows to coexist.
func registerUsageLegSequenceMigration() {
	migrations.MustRegister(usageLegSequenceSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func usageLegSequenceSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing usage-leg sequence schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = []string{
			`ALTER TABLE usage_leg_records ADD COLUMN attempt_seq INTEGER NULL`,
			`CREATE UNIQUE INDEX IF NOT EXISTS ` + usageLegCallAttemptSeqIndex + ` ON usage_leg_records(call_id, attempt_seq)`,
		}
	case dialect.PG:
		statements = []string{
			`ALTER TABLE usage_leg_records ADD COLUMN IF NOT EXISTS attempt_seq BIGINT NULL`,
			`CREATE UNIQUE INDEX IF NOT EXISTS ` + usageLegCallAttemptSeqIndex + ` ON usage_leg_records(call_id, attempt_seq)`,
		}
	default:
		return fmt.Errorf("billing usage-leg sequence schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "duplicate column") || strings.Contains(lower, "already exists") {
				continue
			}
			return fmt.Errorf("billing usage-leg sequence DDL: %w", err)
		}
	}
	return nil
}
