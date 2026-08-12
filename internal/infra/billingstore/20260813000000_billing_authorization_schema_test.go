package billingstore

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
)

func TestAuthorizationSchemaMigrationUpgradesLegacySQLiteTable(t *testing.T) {
	dsn := fmt.Sprintf("file:billing-authorization-migration-%d?mode=memory&cache=shared", testSequence.Add(1))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bunDB.Close() })

	_, err = bunDB.ExecContext(context.Background(), `CREATE TABLE authorization_holds (
		hold_key TEXT PRIMARY KEY,
		account_id TEXT NOT NULL,
		tur_key TEXT NOT NULL,
		currency TEXT NOT NULL,
		amount_nano INTEGER NOT NULL,
		status TEXT NOT NULL,
		source_key TEXT NOT NULL,
		fingerprint TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		created_at TEXT NOT NULL,
		closed_at TEXT
	)`)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := authorizationSchemaUp(ctx, bunDB); err != nil {
		t.Fatalf("forward migration: %v", err)
	}
	if err := authorizationSchemaUp(ctx, bunDB); err != nil {
		t.Fatalf("idempotent forward migration: %v", err)
	}
	for _, column := range sqliteAuthorizationColumns() {
		var count int
		if err := bunDB.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('authorization_holds') WHERE name = ?`, column.name).Scan(ctx, &count); err != nil {
			t.Fatalf("column %s lookup: %v", column.name, err)
		}
		if count != 1 {
			t.Fatalf("column %s count = %d, want 1", column.name, count)
		}
	}
}
