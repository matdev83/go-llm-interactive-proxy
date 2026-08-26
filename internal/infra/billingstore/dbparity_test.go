package billingstore

import (
	"context"
	"testing"
)

// TestDBParity_SQLite is the canonical parity entry point for billing on SQLite.
func TestDBParity_SQLite(t *testing.T) {
	t.Parallel()
	t.Run("CreateAndVerifySchema", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteTestStore(t)
		if err := VerifySchema(context.Background(), store.db); err != nil {
			t.Fatalf("VerifySchema sqlite: %v", err)
		}
	})
}
