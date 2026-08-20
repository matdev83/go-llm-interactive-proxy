package billingstore

import (
	"context"
	"testing"
)

func TestUsageCallRecordsSchemaCreatesCallIDAndJoinIndexes(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	for _, index := range []string{usageCallCallIDIndex, usageCallAccountSessionIndex, usageCallClaimStatusIndex} {
		var name string
		if err := store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(ctx, &name); err != nil {
			t.Fatalf("index %s lookup: %v", index, err)
		}
		if name != index {
			t.Fatalf("index = %q, want %q", name, index)
		}
	}
	if err := usageCallRecordsSchemaUp(ctx, store.db); err != nil {
		t.Fatalf("usage-call schema idempotent: %v", err)
	}
	if err := VerifySchema(ctx, store.db); err != nil {
		t.Fatalf("VerifySchema: %v", err)
	}
	var aggregate int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name LIKE '%usage_call%counter%'`).Scan(ctx, &aggregate); err != nil {
		t.Fatal(err)
	}
	if aggregate != 0 {
		t.Fatal("usage spool must not add a mutable aggregate counter")
	}
}
