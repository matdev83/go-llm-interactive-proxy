//go:build integration

package journalstore_test

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/dbparity"
	"github.com/stretchr/testify/require"
)

// TestDBParity_PostgresDirect is the canonical parity entry point for metering-journal on PostgreSQL Direct.
func TestDBParity_PostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()

	t.Run("CreateAndClose", func(t *testing.T) {
		t.Parallel()
		store := newPostgresJournal(t, dsn, "parity-pg-"+testkit.UniquePostgresStoreID("journal"))
		_ = store
	})
	t.Run("AppendIdempotent", func(t *testing.T) {
		t.Parallel()
		store := newPostgresJournal(t, dsn, "parity-pg-append-"+testkit.UniquePostgresStoreID("journal"))
		f := validFact("parity-fact-pg", "parity-stream-pg", 1)
		if err := store.Append(ctx, f); err != nil {
			t.Fatalf("Append pg: %v", err)
		}
		if err := store.Append(ctx, f); err != nil {
			t.Fatalf("Append idempotent pg: %v", err)
		}
	})
	t.Run("MigrationAndSchemaParity", func(t *testing.T) {
		bunDB := testkit.OpenPostgresBunForTest(t, dsn, 4)
		defer bunDB.Close()
		require.NoError(t, journalstore.Migrate(ctx, bunDB))
		require.NoError(t, dbparity.VerifySchema(ctx, bunDB, journalstore.MeteringJournalLogicalSchemaSpec()))

		_, thisFile, _, ok := runtime.Caller(0)
		require.True(t, ok)
		discovered, err := dbparity.DiscoverMigrations(filepath.Dir(thisFile))
		require.NoError(t, err)
		require.NotEmpty(t, discovered)

		var names []string
		rows, err := bunDB.QueryContext(ctx, "SELECT name FROM bun_metering_journal_migrations")
		require.NoError(t, err)
		defer rows.Close()
		recorded := make(map[string]bool)
		for rows.Next() {
			var name string
			require.NoError(t, rows.Scan(&name))
			names = append(names, name)
			id := name
			if len(name) >= 14 {
				id = name[:14]
			}
			recorded[id] = true
		}
		require.NoError(t, dbparity.AssertMigrationHistoryIDs(dbparity.MigrationIDs(discovered), recorded))

		// Verify migration rerun idempotency
		require.NoError(t, journalstore.Migrate(ctx, bunDB))
		var countAfter int
		require.NoError(t, bunDB.NewRaw("SELECT count(*) FROM bun_metering_journal_migrations").Scan(ctx, &countAfter))
		require.Equal(t, len(names), countAfter)
	})
}
