package authoritystore_test

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore/contract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/dbparity"
	"github.com/stretchr/testify/require"
)

// TestDBParity_SQLite is the canonical parity entry point for usage-authority on SQLite.
func TestDBParity_SQLite(t *testing.T) {
	t.Parallel()

	t.Run("Contract", func(t *testing.T) {
		t.Parallel()
		contract.RunSuite(t, sqliteParityFactory{})
	})

	t.Run("MigrationAndSchemaParity", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		dsn := sqliteAuthorityDSN(filepath.Join(t.TempDir(), "parity.db"))
		bunDB := openSQLiteAuthorityBun(t, dsn)
		defer func() { _ = bunDB.Close() }()
		require.NoError(t, authoritystore.Migrate(ctx, bunDB))
		require.NoError(t, dbparity.VerifySchema(ctx, bunDB, authoritystore.UsageAuthorityLogicalSchemaSpec()))

		_, thisFile, _, ok := runtime.Caller(0)
		require.True(t, ok)
		discovered, err := dbparity.DiscoverMigrations(filepath.Dir(thisFile))
		require.NoError(t, err)
		require.NotEmpty(t, discovered)

		var names []string
		rows, err := bunDB.QueryContext(ctx, "SELECT name FROM bun_usage_authority_migrations")
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
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
		require.NoError(t, authoritystore.Migrate(ctx, bunDB))
		var countAfter int
		require.NoError(t, bunDB.NewRaw("SELECT count(*) FROM bun_usage_authority_migrations").Scan(ctx, &countAfter))
		require.Equal(t, len(names), countAfter)
	})
}

type sqliteParityFactory struct{}

func (sqliteParityFactory) Build(t *testing.T) app.StateStore {
	t.Helper()
	dsn := sqliteAuthorityDSN(filepath.Join(t.TempDir(), "usage.db"))
	bunDB := openSQLiteAuthorityBun(t, dsn)
	cfg := authoritystore.Config{StoreID: "parity", Backing: domain.BackingCapabilityAtomic, LimitRows: contract.SeededLimitRows(), Readiness: contract.SeededReadiness()}
	store, err := authoritystore.NewDurable(context.Background(), bunDB, cfg)
	if err != nil {
		t.Fatalf("NewDurable sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
