package billingstore

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
)

func TestPhase4SchemaMigrationUpgradesPrePhase4SQLiteSchema(t *testing.T) {
	dsn := fmt.Sprintf("file:billing-phase4-migration-%d?mode=memory&cache=shared", testSequence.Add(1))
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
	ctx := context.Background()
	if err := baselineUp(ctx, bunDB); err != nil {
		t.Fatal(err)
	}
	if err := authorizationSchemaUp(ctx, bunDB); err != nil {
		t.Fatal(err)
	}
	if err := phase4SchemaUp(ctx, bunDB); err != nil {
		t.Fatalf("phase4 upgrade: %v", err)
	}
	if err := phase4SchemaUp(ctx, bunDB); err != nil {
		t.Fatalf("phase4 idempotent upgrade: %v", err)
	}
	for _, column := range []string{"released_amount_nano", "closed_source_key", "closed_fingerprint", "closed_amount_nano"} {
		var count int
		if err := bunDB.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('authorization_holds') WHERE name = ?`, column).Scan(ctx, &count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("hold column %s count=%d", column, count)
		}
	}
	var table string
	if err := bunDB.NewRaw(`SELECT name FROM sqlite_master WHERE type='table' AND name='billing_operation_snapshots'`).Scan(ctx, &table); err != nil {
		t.Fatal(err)
	}
	if table != "billing_operation_snapshots" {
		t.Fatalf("snapshot table = %q", table)
	}
	var integrityCol int
	if err := bunDB.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('billing_operation_snapshots') WHERE name = 'integrity_fingerprint'`).Scan(ctx, &integrityCol); err != nil {
		t.Fatal(err)
	}
	if integrityCol != 1 {
		t.Fatalf("integrity_fingerprint column count=%d", integrityCol)
	}
}
