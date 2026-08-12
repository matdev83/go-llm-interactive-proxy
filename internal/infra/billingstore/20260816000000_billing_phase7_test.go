package billingstore

import (
	"context"
	"testing"
)

func TestPhase7SchemaCreatesReversalUniqueIndex(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	var name string
	if err := store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, journalReversalUniqueIndex).Scan(ctx, &name); err != nil {
		t.Fatalf("reversal unique index lookup: %v", err)
	}
	if name != journalReversalUniqueIndex {
		t.Fatalf("index = %q, want %q", name, journalReversalUniqueIndex)
	}
	if err := phase7SchemaUp(ctx, store.db); err != nil {
		t.Fatalf("phase7 idempotent: %v", err)
	}
	if err := VerifySchema(ctx, store.db); err != nil {
		t.Fatalf("VerifySchema: %v", err)
	}
}
