package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity/sqlitestore"
)

func TestMigrate_freshCreatesTablesAndIndexes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openSchemaTestDB(t)
	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	wantTables := []string{
		"lip_secure_attempt_traces",
		"lip_secure_audit",
		"lip_secure_sessions",
		"lip_secure_transcript",
		"lip_secure_turns",
		"lip_secure_usage",
	}
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if strings.HasPrefix(name, "lip_secure_") {
			got = append(got, name)
		}
	}
	_ = rows.Close()
	slices.Sort(got)
	if !slices.Equal(got, wantTables) {
		t.Fatalf("tables\ngot  %#v\nwant %#v", got, wantTables)
	}

	// Unique on session id is PRIMARY KEY; fingerprint unique index must exist.
	var idxCount int
	err = db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_lip_secure_sessions_resume_fp'`).Scan(&idxCount)
	if err != nil {
		t.Fatal(err)
	}
	if idxCount != 1 {
		t.Fatalf("expected resume fingerprint unique index, got count %d", idxCount)
	}
	err = db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_lip_secure_sessions_a_leg_unique'`).Scan(&idxCount)
	if err != nil {
		t.Fatal(err)
	}
	if idxCount != 1 {
		t.Fatalf("expected partial unique a_leg index, got count %d", idxCount)
	}
}

func TestMigrate_idempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openSchemaTestDB(t)
	for range 3 {
		if err := migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrate_coexistsWithContinuity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.db")

	dsn, err := dsnFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)

	cont, err := sqlitestore.New(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cont.Close() })

	leg, err := cont.CreateALeg(ctx, "ck-coexist")
	if err != nil {
		t.Fatal(err)
	}
	if leg.ALegID == "" {
		t.Fatal("expected continuity a-leg")
	}

	if _, err := New(db); err != nil {
		t.Fatal(err)
	}

	var nCont, nSecure int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name='a_legs'`).Scan(&nCont); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name='lip_secure_sessions'`).Scan(&nSecure); err != nil {
		t.Fatal(err)
	}
	if nCont != 1 || nSecure != 1 {
		t.Fatalf("continuity table %d secure table %d", nCont, nSecure)
	}

	got, err := cont.ResolveALeg(ctx, "ck-coexist")
	if err != nil {
		t.Fatal(err)
	}
	if got.ALegID != leg.ALegID {
		t.Fatalf("continuity broken: got %q want %q", got.ALegID, leg.ALegID)
	}
}

func TestMigrate_upgradeQuarantineColumns_addsToLegacySessionsTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	dsn, err := dsnFromPath(filepath.Join(dir, "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	_, err = db.ExecContext(ctx, `CREATE TABLE lip_secure_sessions (
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
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgradeQuarantineColumns(ctx, db); err != nil {
		t.Fatal(err)
	}
	// Idempotent second upgrade.
	if err := upgradeQuarantineColumns(ctx, db); err != nil {
		t.Fatal(err)
	}
	needed := []string{"status", "quarantined_at_unix", "quarantine_reason_code", "quarantine_event_id"}
	for _, col := range needed {
		var n int
		err := db.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM pragma_table_info('lip_secure_sessions') WHERE name = ?`, col,
		).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("expected column %q after upgrade, count=%d", col, n)
		}
	}
}

func openSchemaTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dsn, err := dsnFromPath(filepath.Join(dir, "schema.db"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	return db
}
