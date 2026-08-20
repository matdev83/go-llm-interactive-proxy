package billingstore

import (
	"context"
	"testing"
)

func TestUsageLegRecordsSchemaCreatesCallBLegUniqueIndex(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	var name string
	if err := store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, usageLegCallBLegIndex).Scan(ctx, &name); err != nil {
		t.Fatalf("call/b-leg unique index lookup: %v", err)
	}
	if name != usageLegCallBLegIndex {
		t.Fatalf("index = %q, want %q", name, usageLegCallBLegIndex)
	}
	if err := usageLegRecordsSchemaUp(ctx, store.db); err != nil {
		t.Fatalf("usage-leg schema idempotent: %v", err)
	}
	if err := VerifySchema(ctx, store.db); err != nil {
		t.Fatalf("VerifySchema: %v", err)
	}
	var aggregate int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name LIKE '%usage_leg%counter%'`).Scan(ctx, &aggregate); err != nil {
		t.Fatal(err)
	}
	if aggregate != 0 {
		t.Fatal("usage spool must not add a mutable aggregate counter")
	}
}
