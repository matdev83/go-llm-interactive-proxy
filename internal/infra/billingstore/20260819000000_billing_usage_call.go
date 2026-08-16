package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const UsageCallRecordsMigrationName = "20260819000000"

const (
	usageCallCallIDIndex         = "idx_usage_call_records_call_id"
	usageCallAccountSessionIndex = "idx_usage_call_records_account_session"
	usageCallClaimStatusIndex    = "idx_usage_call_records_claim_status"
)

func registerUsageCallRecordsMigration() {
	migrations.MustRegister(usageCallRecordsSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func usageCallRecordsSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing usage-call schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = sqliteUsageCallRecordsDDL()
	case dialect.PG:
		statements = postgresUsageCallRecordsDDL()
	default:
		return fmt.Errorf("billing usage-call schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "duplicate column") || strings.Contains(lower, "already exists") {
				continue
			}
			return fmt.Errorf("billing usage-call DDL: %w", err)
		}
	}
	return nil
}

func sqliteUsageCallRecordsDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS usage_call_records (
			usage_call_key TEXT PRIMARY KEY,
			fingerprint TEXT NOT NULL,
			call_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			a_leg_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT NOT NULL,
			outcome TEXT NOT NULL,
			expected_b_leg_ids TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			sealed_at TEXT NOT NULL,
			claim_status TEXT NOT NULL DEFAULT 'pending',
			claimed_at TEXT,
			claim_attempt_count INTEGER NOT NULL DEFAULT 0,
			next_claim_at TEXT NOT NULL,
			last_claim_error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + usageCallCallIDIndex + ` ON usage_call_records(call_id)`,
		`CREATE INDEX IF NOT EXISTS ` + usageCallAccountSessionIndex + ` ON usage_call_records(account_id, session_id, call_id)`,
		`CREATE INDEX IF NOT EXISTS ` + usageCallClaimStatusIndex + ` ON usage_call_records(claim_status, sealed_at)`,
		`CREATE INDEX IF NOT EXISTS ` + usageCallClaimPendingIndex + ` ON usage_call_records(claim_status, next_claim_at, sealed_at, call_id)`,
		`CREATE TRIGGER IF NOT EXISTS billing_usage_call_immutable_update BEFORE UPDATE ON usage_call_records
			WHEN NEW.usage_call_key IS NOT OLD.usage_call_key
			 OR NEW.fingerprint IS NOT OLD.fingerprint
			 OR NEW.call_id IS NOT OLD.call_id
			 OR NEW.account_id IS NOT OLD.account_id
			 OR NEW.a_leg_id IS NOT OLD.a_leg_id
			 OR NEW.session_id IS NOT OLD.session_id
			 OR NEW.started_at IS NOT OLD.started_at
			 OR NEW.finished_at IS NOT OLD.finished_at
			 OR NEW.outcome IS NOT OLD.outcome
			 OR NEW.expected_b_leg_ids IS NOT OLD.expected_b_leg_ids
			 OR NEW.payload_json IS NOT OLD.payload_json
			 OR NEW.sealed_at IS NOT OLD.sealed_at
			BEGIN SELECT RAISE(ABORT, 'usage call records are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_usage_call_immutable_delete BEFORE DELETE ON usage_call_records BEGIN SELECT RAISE(ABORT, 'usage call records are immutable'); END`,
	}
}

func postgresUsageCallRecordsDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS usage_call_records (
			usage_call_key TEXT PRIMARY KEY,
			fingerprint TEXT NOT NULL,
			call_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			a_leg_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT NOT NULL,
			outcome TEXT NOT NULL,
			expected_b_leg_ids TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			sealed_at TEXT NOT NULL,
			claim_status TEXT NOT NULL DEFAULT 'pending',
			claimed_at TEXT,
			claim_attempt_count INTEGER NOT NULL DEFAULT 0,
			next_claim_at TEXT NOT NULL,
			last_claim_error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + usageCallCallIDIndex + ` ON usage_call_records(call_id)`,
		`CREATE INDEX IF NOT EXISTS ` + usageCallAccountSessionIndex + ` ON usage_call_records(account_id, session_id, call_id)`,
		`CREATE INDEX IF NOT EXISTS ` + usageCallClaimStatusIndex + ` ON usage_call_records(claim_status, sealed_at)`,
		`CREATE INDEX IF NOT EXISTS ` + usageCallClaimPendingIndex + ` ON usage_call_records(claim_status, next_claim_at, sealed_at, call_id)`,
		`CREATE OR REPLACE FUNCTION billing_reject_usage_call_payload_immutable() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'usage call records are immutable';
  END IF;
  IF NEW.usage_call_key IS DISTINCT FROM OLD.usage_call_key
     OR NEW.fingerprint IS DISTINCT FROM OLD.fingerprint
     OR NEW.call_id IS DISTINCT FROM OLD.call_id
     OR NEW.account_id IS DISTINCT FROM OLD.account_id
     OR NEW.a_leg_id IS DISTINCT FROM OLD.a_leg_id
     OR NEW.session_id IS DISTINCT FROM OLD.session_id
     OR NEW.started_at IS DISTINCT FROM OLD.started_at
     OR NEW.finished_at IS DISTINCT FROM OLD.finished_at
     OR NEW.outcome IS DISTINCT FROM OLD.outcome
     OR NEW.expected_b_leg_ids IS DISTINCT FROM OLD.expected_b_leg_ids
     OR NEW.payload_json IS DISTINCT FROM OLD.payload_json
     OR NEW.sealed_at IS DISTINCT FROM OLD.sealed_at THEN
    RAISE EXCEPTION 'usage call records are immutable';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS billing_usage_call_immutable ON usage_call_records`,
		`CREATE TRIGGER billing_usage_call_immutable BEFORE UPDATE OR DELETE ON usage_call_records FOR EACH ROW EXECUTE FUNCTION billing_reject_usage_call_payload_immutable()`,
	}
}
