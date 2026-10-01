package billingstore

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/uptrace/bun"
)

type schemaDDLHook struct{ statements int }

func (*schemaDDLHook) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context { return ctx }
func (h *schemaDDLHook) AfterQuery(_ context.Context, event *bun.QueryEvent) {
	if strings.HasPrefix(strings.TrimSpace(event.Query), "CREATE ") {
		h.statements++
	}
}

func TestSchemaFixtureClonesWithoutReparsingDDL(t *testing.T) {
	ctx := t.Context()
	dsn := fmt.Sprintf("file:clone-proof-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)&_txlock=immediate", testSequence.Add(1))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	hook := &schemaDDLHook{}
	bunDB.AddQueryHook(hook)
	seedTestSchemaIfEmpty(t, bunDB)
	if hook.statements != 0 {
		t.Fatalf("fixture reparsed %d CREATE statements; want native schema clone", hook.statements)
	}
	// A second simultaneously open connection must see the same cloned schema.
	first, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	second, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	var count, foreignKeys int
	if err := second.QueryRowContext(ctx, "SELECT COUNT(*) FROM bun_billing_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(testSchemaRows) {
		t.Fatalf("migration history=%d want %d", count, len(testSchemaRows))
	}
	if err := second.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatal("clone lost per-connection foreign key policy")
	}
}

func BenchmarkSQLiteSchemaFixture(b *testing.B) {
	for b.Loop() {
		sqlDB, err := sql.Open("sqlite", fmt.Sprintf("file:schema-bench-%d?mode=memory&cache=shared", testSequence.Add(1)))
		if err != nil {
			b.Fatal(err)
		}
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			b.Fatal(err)
		}
		seedTestSchemaIfEmpty(b, bunDB)
		if err := bunDB.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func TestSchemaCloneMatchesRealMigrationAndKeepsPrivateMutation(t *testing.T) {
	ctx := t.Context()
	clone := newSQLiteTestStore(t)
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	reference, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	// Independent expectation: execute the actual migration chain on a new DB.
	if err := Migrate(ctx, reference); err != nil {
		t.Fatal(err)
	}
	query := "SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_master ORDER BY type, name"
	read := func(database *sql.DB) []string {
		rows, err := database.QueryContext(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var objects []string
		for rows.Next() {
			var kind, name, table, ddl string
			if err := rows.Scan(&kind, &name, &table, &ddl); err != nil {
				t.Fatal(err)
			}
			objects = append(objects, strings.Join([]string{kind, name, table, ddl}, "\x00"))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return objects
	}
	if got, want := read(clone.db.DB), read(sqlDB); !reflect.DeepEqual(got, want) {
		t.Fatalf("cloned schema differs from real migrations:\ngot %v\nwant %v", got, want)
	}
	if _, err := clone.db.ExecContext(ctx, "CREATE TABLE fixture_private(value TEXT)"); err != nil {
		t.Fatal(err)
	}
	other := newSQLiteTestStore(t)
	var count int
	if err := other.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name='fixture_private'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("fixture mutation escaped into another clone")
	}
}
