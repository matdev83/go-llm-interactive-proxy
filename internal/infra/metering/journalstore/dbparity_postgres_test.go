//go:build integration

package journalstore_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// TestDBParity_PostgresDirect is the canonical parity entry point for metering-journal on PostgreSQL Direct.
func TestDBParity_PostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	t.Run("CreateAndClose", func(t *testing.T) {
		t.Parallel()
		store := newPostgresJournal(t, dsn, "parity-pg-"+testkit.UniquePostgresStoreID("journal"))
		_ = store
	})
	t.Run("AppendIdempotent", func(t *testing.T) {
		t.Parallel()
		store := newPostgresJournal(t, dsn, "parity-pg-append-"+testkit.UniquePostgresStoreID("journal"))
		ctx := context.Background()
		f := validFact("parity-fact-pg", "parity-stream-pg", 1)
		if err := store.Append(ctx, f); err != nil {
			t.Fatalf("Append pg: %v", err)
		}
		if err := store.Append(ctx, f); err != nil {
			t.Fatalf("Append idempotent pg: %v", err)
		}
	})
}
