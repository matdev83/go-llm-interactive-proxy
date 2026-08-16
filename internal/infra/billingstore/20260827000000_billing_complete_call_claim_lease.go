package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const CompleteCallClaimLeaseMigrationName = "20260827000000"

const usageCallClaimPendingIndex = "idx_usage_call_records_claim_pending"

func registerCompleteCallClaimLeaseMigration() {
	migrations.MustRegister(completeCallClaimLeaseSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func completeCallClaimLeaseSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing complete-call claim lease schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = []string{
			`ALTER TABLE usage_call_records ADD COLUMN claim_attempt_count INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE usage_call_records ADD COLUMN next_claim_at TEXT NOT NULL DEFAULT ''`,
			`UPDATE usage_call_records SET next_claim_at = sealed_at WHERE next_claim_at = ''`,
			`ALTER TABLE usage_call_records ADD COLUMN last_claim_error TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX IF NOT EXISTS ` + usageCallClaimPendingIndex + ` ON usage_call_records(claim_status, next_claim_at, sealed_at, call_id)`,
		}
	case dialect.PG:
		statements = []string{
			`ALTER TABLE usage_call_records ADD COLUMN IF NOT EXISTS claim_attempt_count INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE usage_call_records ADD COLUMN IF NOT EXISTS next_claim_at TEXT NOT NULL DEFAULT ''`,
			`UPDATE usage_call_records SET next_claim_at = sealed_at WHERE next_claim_at = ''`,
			`ALTER TABLE usage_call_records ADD COLUMN IF NOT EXISTS last_claim_error TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX IF NOT EXISTS ` + usageCallClaimPendingIndex + ` ON usage_call_records(claim_status, next_claim_at, sealed_at, call_id)`,
		}
	default:
		return fmt.Errorf("billing complete-call claim lease schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "duplicate column") || strings.Contains(lower, "already exists") {
				continue
			}
			return fmt.Errorf("billing complete-call claim lease DDL: %w", err)
		}
	}
	return nil
}
