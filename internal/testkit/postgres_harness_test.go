package testkit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uptrace/bun"
)

func TestUniquePostgresStoreID_isGloballyDistinct(t *testing.T) {
	a := UniquePostgresStoreID("auth")
	b := UniquePostgresStoreID("auth")
	if a == "" || b == "" || a == b {
		t.Fatalf("want distinct non-empty ids; a=%q b=%q", a, b)
	}
	if !strings.HasPrefix(a, "auth-") || !strings.HasPrefix(b, "auth-") {
		t.Fatalf("prefix missing: a=%q b=%q", a, b)
	}
}

func TestClassifyForbiddenRuntimeSQL(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		want    string
		wantAny bool // non-empty reason required; exact string optional
	}{
		{name: "select_ok", query: `SELECT row_json FROM usage_authority_limit_rows WHERE store_id=?`, want: ""},
		{name: "insert_ok", query: `INSERT INTO concurrency_leases(store_id, lease_id) VALUES (?,?)`, want: ""},
		{name: "update_ok", query: `UPDATE metering_facts SET store_id=? WHERE fact_id=?`, want: ""},
		{name: "delete_ok", query: `DELETE FROM concurrency_leases WHERE store_id=?`, want: ""},
		{name: "set_transaction_ok", query: `SET TRANSACTION ISOLATION LEVEL READ COMMITTED`, want: ""},
		{name: "set_transaction_lower", query: `set transaction read only`, want: ""},
		{name: "advisory_xact_ok", query: `SELECT pg_advisory_xact_lock(42)`, want: ""},
		{name: "try_advisory_xact_ok", query: `SELECT pg_try_advisory_xact_lock(1, 2)`, want: ""},
		{name: "leading_whitespace_create", query: "  \n\tCREATE TABLE IF NOT EXISTS concurrency_leases (store_id TEXT)", wantAny: true},
		{name: "line_comment_then_create", query: "-- bootstrap\nCREATE TABLE t(id INT)", wantAny: true},
		{name: "block_comment_then_alter", query: "/* schema */ ALTER TABLE metering_facts ADD COLUMN x TEXT", wantAny: true},
		{name: "create_table", query: `CREATE TABLE IF NOT EXISTS concurrency_leases (store_id TEXT)`, wantAny: true},
		{name: "create_index", query: `CREATE INDEX IF NOT EXISTS idx_x ON concurrency_leases(store_id)`, wantAny: true},
		{name: "alter", query: `ALTER TABLE metering_facts ADD COLUMN x TEXT`, wantAny: true},
		{name: "drop", query: `DROP TABLE metering_facts`, wantAny: true},
		{name: "search_path", query: `SET search_path TO lease_test_1`, wantAny: true},
		{name: "set_local", query: `SET LOCAL work_mem = '64MB'`, wantAny: true},
		{name: "set_guc", query: `SET work_mem = '64MB'`, wantAny: true},
		{name: "reset", query: `RESET search_path`, wantAny: true},
		{name: "reset_all", query: `RESET ALL`, wantAny: true},
		{name: "prepare", query: `PREPARE foo AS SELECT 1`, wantAny: true},
		{name: "deallocate", query: `DEALLOCATE foo`, wantAny: true},
		{name: "temp_table", query: `CREATE TEMP TABLE tmp (id INT)`, wantAny: true},
		{name: "temporary_table", query: `CREATE TEMPORARY TABLE tmp (id INT)`, wantAny: true},
		{name: "session_advisory_lock", query: `SELECT pg_advisory_lock(99)`, wantAny: true},
		{name: "session_try_advisory_lock", query: `SELECT pg_try_advisory_lock(1, 2)`, wantAny: true},
		{name: "session_advisory_unlock", query: `SELECT pg_advisory_unlock(99)`, wantAny: true},
		{name: "literal_contains_create_word", query: `SELECT ' create table evil' AS note`, want: ""},
		{name: "comment_contains_drop_word", query: `SELECT 1 /* drop table evil */`, want: ""},
		{name: "line_comment_contains_alter", query: "SELECT 1 -- alter table evil", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyForbiddenRuntimeSQL(tc.query)
			if tc.wantAny {
				if got == "" {
					t.Fatalf("query %q: want forbidden reason, got empty", tc.query)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("query %q: got %q want %q", tc.query, got, tc.want)
			}
		})
	}
}

func TestRuntimeSQLGuard_recordsViolations(t *testing.T) {
	guard := NewRuntimeSQLGuard()
	_ = guard.BeforeQuery(t.Context(), &bun.QueryEvent{Query: `SET search_path TO public`})
	_ = guard.BeforeQuery(t.Context(), &bun.QueryEvent{Query: `SELECT 1`})
	violations := guard.Violations()
	if len(violations) != 1 {
		t.Fatalf("violations=%v", violations)
	}
}

func TestCleanupStatementsJournalUsesStoreScopedFilters(t *testing.T) {
	const storeID = "store-under-test"

	statements := cleanupStatements(PostgresComponentJournal, storeID)
	if len(statements) != 2 {
		t.Fatalf("journal cleanup statements=%d want 2", len(statements))
	}

	filterDelete := strings.Join(strings.Fields(statements[0].sql), " ")
	want := "DELETE FROM metering_fact_filters WHERE store_id = ?"
	if filterDelete != want {
		t.Fatalf("filter cleanup %q want %q", filterDelete, want)
	}
	if len(statements[0].args) != 1 || statements[0].args[0] != storeID {
		t.Fatalf("filter cleanup args=%v want [%q]", statements[0].args, storeID)
	}
}

func TestAssertNoSearchPathInDualPlanePostgresSources(t *testing.T) {
	root := moduleRoot(t)
	for _, relDir := range DualPlanePostgresSearchPathGuardDirs() {
		dir := filepath.Join(root, filepath.FromSlash(relDir))
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(body), "search_path") {
				rel, _ := filepath.Rel(root, path)
				return fmt.Errorf("%s must not reference search_path; use unique store_id + admin cleanup", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
