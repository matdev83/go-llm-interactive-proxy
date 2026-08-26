package journalstore_test

import (
	"context"
	"testing"
)

// TestDBParity_SQLite is the canonical parity entry point for metering-journal on SQLite.
func TestDBParity_SQLite(t *testing.T) {
	t.Parallel()
	t.Run("CreateAndClose", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteJournal(t)
		_ = store
	})
	t.Run("AppendIdempotent", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteJournal(t)
		ctx := context.Background()
		f := validFact("parity-fact", "parity-stream", 1)
		if err := store.Append(ctx, f); err != nil {
			t.Fatalf("Append: %v", err)
		}
		if err := store.Append(ctx, f); err != nil {
			t.Fatalf("Append idempotent: %v", err)
		}
	})
}
