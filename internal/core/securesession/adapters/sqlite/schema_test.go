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
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
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
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
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
