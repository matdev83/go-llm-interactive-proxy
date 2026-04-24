package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// migrate applies secure-session DDL. Safe to call multiple times (IF NOT EXISTS).
func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("securesession/sqlite migrate pragma: %w", err)
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS lip_secure_sessions (
			session_id TEXT NOT NULL PRIMARY KEY,
			resume_fingerprint BLOB NOT NULL,
			owner_id TEXT NOT NULL DEFAULT '',
			owner_issuer TEXT NOT NULL DEFAULT '',
			owner_tenant TEXT NOT NULL DEFAULT '',
			workspace_id TEXT NOT NULL DEFAULT '',
			client_session_id TEXT NOT NULL DEFAULT '',
			agent_digest TEXT NOT NULL DEFAULT '',
			policy_version TEXT NOT NULL DEFAULT '',
			transcript_enabled INTEGER NOT NULL DEFAULT 0,
			effective_treatment TEXT NOT NULL DEFAULT '',
			stricter_policy_resolution TEXT NOT NULL DEFAULT '',
			route_hint TEXT NOT NULL DEFAULT '',
			redaction_profile TEXT NOT NULL DEFAULT '',
			audit_mode TEXT NOT NULL DEFAULT '',
			a_leg_id TEXT NOT NULL DEFAULT '',
			resume_eligible INTEGER NOT NULL DEFAULT 0,
			last_activity_unix INTEGER NOT NULL,
			last_activity_source TEXT NOT NULL DEFAULT '',
			created_at_unix INTEGER NOT NULL,
			usage_in BIGINT NOT NULL DEFAULT 0,
			usage_out BIGINT NOT NULL DEFAULT 0,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			latest_attempt_trace_json TEXT NOT NULL DEFAULT '{}',
			latest_attempt_outcome_json TEXT NOT NULL DEFAULT '{}',
			latest_attempt_accounting_json TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_lip_secure_sessions_resume_fp
			ON lip_secure_sessions(resume_fingerprint)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_lip_secure_sessions_a_leg_unique
			ON lip_secure_sessions(a_leg_id) WHERE a_leg_id != ''`,
		`CREATE INDEX IF NOT EXISTS idx_lip_secure_sessions_owner
			ON lip_secure_sessions(owner_id)`,
		`CREATE INDEX IF NOT EXISTS idx_lip_secure_sessions_workspace
			ON lip_secure_sessions(workspace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_lip_secure_sessions_owner_workspace
			ON lip_secure_sessions(owner_id, workspace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_lip_secure_sessions_last_activity
			ON lip_secure_sessions(last_activity_unix DESC)`,

		`CREATE TABLE IF NOT EXISTS lip_secure_turns (
			session_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			PRIMARY KEY(session_id, turn_id),
			FOREIGN KEY(session_id) REFERENCES lip_secure_sessions(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_lip_secure_turns_session ON lip_secure_turns(session_id)`,

		`CREATE TABLE IF NOT EXISTS lip_secure_attempt_traces (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			a_leg_id TEXT NOT NULL,
			b_leg_id TEXT NOT NULL,
			attempt_seq INTEGER NOT NULL,
			requested_model TEXT NOT NULL DEFAULT '',
			requested_alias TEXT NOT NULL DEFAULT '',
			resolved_backend TEXT NOT NULL DEFAULT '',
			resolved_model TEXT NOT NULL DEFAULT '',
			route_source TEXT NOT NULL DEFAULT '',
			route_reason TEXT NOT NULL DEFAULT '',
			settings_json TEXT NOT NULL DEFAULT '{}',
			started_at_unix INTEGER NOT NULL,
			ended_at_unix INTEGER NOT NULL DEFAULT 0,
			success INTEGER NOT NULL DEFAULT 0,
			surface_state TEXT NOT NULL DEFAULT '',
			http_status INTEGER NOT NULL DEFAULT 0,
			provider_status TEXT NOT NULL DEFAULT '',
			error_code TEXT NOT NULL DEFAULT '',
			timeout_class TEXT NOT NULL DEFAULT '',
			debug_reason TEXT NOT NULL DEFAULT '',
			outcome_json TEXT NOT NULL DEFAULT '{}',
			FOREIGN KEY(session_id) REFERENCES lip_secure_sessions(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_lip_secure_attempt_traces_session
			ON lip_secure_attempt_traces(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_lip_secure_attempt_traces_b_leg
			ON lip_secure_attempt_traces(b_leg_id)`,
		`CREATE INDEX IF NOT EXISTS idx_lip_secure_attempt_traces_resolved
			ON lip_secure_attempt_traces(resolved_backend, resolved_model)`,

		`CREATE TABLE IF NOT EXISTS lip_secure_transcript (
			session_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			turn_id TEXT NOT NULL,
			event_kind TEXT NOT NULL,
			payload_ref TEXT NOT NULL DEFAULT '',
			created_at_unix INTEGER NOT NULL,
			PRIMARY KEY(session_id, seq),
			FOREIGN KEY(session_id) REFERENCES lip_secure_sessions(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_lip_secure_transcript_session_seq
			ON lip_secure_transcript(session_id, seq)`,

		`CREATE TABLE IF NOT EXISTS lip_secure_usage (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			b_leg_id TEXT NOT NULL DEFAULT '',
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_write_tokens INTEGER NOT NULL DEFAULT 0,
			cost_minor_units INTEGER NOT NULL DEFAULT 0,
			currency TEXT NOT NULL DEFAULT '',
			billing_unavailable INTEGER NOT NULL DEFAULT 0,
			created_at_unix INTEGER NOT NULL,
			FOREIGN KEY(session_id) REFERENCES lip_secure_sessions(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_lip_secure_usage_session ON lip_secure_usage(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_lip_secure_usage_b_leg ON lip_secure_usage(b_leg_id)`,

		`CREATE TABLE IF NOT EXISTS lip_secure_audit (
			session_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			turn_id TEXT NOT NULL,
			action TEXT NOT NULL,
			result TEXT NOT NULL DEFAULT '',
			created_at_unix INTEGER NOT NULL,
			PRIMARY KEY(session_id, seq),
			FOREIGN KEY(session_id) REFERENCES lip_secure_sessions(session_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_lip_secure_audit_session_seq
			ON lip_secure_audit(session_id, seq)`,
	}
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("securesession/sqlite migrate: %w", err)
		}
	}
	// Upgrade older DBs that created lip_secure_attempt_traces before per-attempt outcome columns.
	if err := upgradeAttemptTraceOutcomeColumns(ctx, db); err != nil {
		return err
	}
	return nil
}

func upgradeAttemptTraceOutcomeColumns(ctx context.Context, db *sql.DB) error {
	alters := []string{
		`ALTER TABLE lip_secure_attempt_traces ADD COLUMN ended_at_unix INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE lip_secure_attempt_traces ADD COLUMN success INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE lip_secure_attempt_traces ADD COLUMN surface_state TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE lip_secure_attempt_traces ADD COLUMN http_status INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE lip_secure_attempt_traces ADD COLUMN provider_status TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE lip_secure_attempt_traces ADD COLUMN error_code TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE lip_secure_attempt_traces ADD COLUMN timeout_class TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE lip_secure_attempt_traces ADD COLUMN debug_reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE lip_secure_attempt_traces ADD COLUMN outcome_json TEXT NOT NULL DEFAULT '{}'`,
	}
	for _, q := range alters {
		if _, err := db.ExecContext(ctx, q); err != nil {
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "duplicate column name") {
				continue
			}
			return fmt.Errorf("securesession/sqlite migrate upgrade attempt_traces: %w", err)
		}
	}
	return nil
}
