package billingstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// SubmissionFeeClaimMigrationName identifies the durable once-per-submission
// commercial-fee claim. The claim is immutable history keyed by store,
// account, and trusted SubmissionID.
const SubmissionFeeClaimMigrationName = "20260917000000"

const submissionFeeClaimScopeIndex = "idx_billing_submission_fee_claims_scope"

func registerSubmissionFeeClaimMigration() {
	migrations.MustRegister(submissionFeeClaimSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func submissionFeeClaimSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("billing submission-fee claim schema: nil database")
	}
	var statements []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statements = submissionFeeClaimSQLiteDDL()
	case dialect.PG:
		statements = submissionFeeClaimPostgresDDL()
	default:
		return fmt.Errorf("billing submission-fee claim schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("billing submission-fee claim DDL: %w", err)
		}
	}
	return nil
}

func submissionFeeClaimSQLiteDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS billing_submission_fee_claims (
			claim_key TEXT PRIMARY KEY,
			store_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			submission_id TEXT NOT NULL,
			source_call_id TEXT NOT NULL,
			tariff_id TEXT NOT NULL,
			tariff_version TEXT NOT NULL,
			tariff_content_ref TEXT NOT NULL DEFAULT '',
			tariff_content_hash TEXT NOT NULL DEFAULT '',
			policy_id TEXT NOT NULL,
			policy_version TEXT NOT NULL,
			policy_content_ref TEXT NOT NULL DEFAULT '',
			policy_content_hash TEXT NOT NULL DEFAULT '',
			context_fingerprint TEXT NOT NULL,
			amount_nano INTEGER NOT NULL CHECK (amount_nano >= 0),
			currency TEXT NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE(store_id, account_id, submission_id),
			FOREIGN KEY(account_id) REFERENCES billing_accounts
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + submissionFeeClaimScopeIndex + ` ON billing_submission_fee_claims(store_id, account_id, submission_id)`,
		`CREATE TRIGGER IF NOT EXISTS billing_submission_fee_claims_immutable_update BEFORE UPDATE ON billing_submission_fee_claims BEGIN SELECT RAISE(ABORT, 'billing submission fee claims are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS billing_submission_fee_claims_immutable_delete BEFORE DELETE ON billing_submission_fee_claims BEGIN SELECT RAISE(ABORT, 'billing submission fee claims are immutable'); END`,
	}
}

func submissionFeeClaimPostgresDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS billing_submission_fee_claims (
			claim_key TEXT PRIMARY KEY,
			store_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			submission_id TEXT NOT NULL,
			source_call_id TEXT NOT NULL,
			tariff_id TEXT NOT NULL,
			tariff_version TEXT NOT NULL,
			tariff_content_ref TEXT NOT NULL DEFAULT '',
			tariff_content_hash TEXT NOT NULL DEFAULT '',
			policy_id TEXT NOT NULL,
			policy_version TEXT NOT NULL,
			policy_content_ref TEXT NOT NULL DEFAULT '',
			policy_content_hash TEXT NOT NULL DEFAULT '',
			context_fingerprint TEXT NOT NULL,
			amount_nano BIGINT NOT NULL CHECK (amount_nano >= 0),
			currency TEXT NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE(store_id, account_id, submission_id),
			FOREIGN KEY(account_id) REFERENCES billing_accounts
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + submissionFeeClaimScopeIndex + ` ON billing_submission_fee_claims(store_id, account_id, submission_id)`,
		`CREATE OR REPLACE FUNCTION billing_reject_submission_fee_claim_mutation() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'billing submission fee claims are immutable'; END; $$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS billing_submission_fee_claims_immutable ON billing_submission_fee_claims`,
		`CREATE TRIGGER billing_submission_fee_claims_immutable BEFORE UPDATE OR DELETE ON billing_submission_fee_claims FOR EACH ROW EXECUTE FUNCTION billing_reject_submission_fee_claim_mutation()`,
	}
}
