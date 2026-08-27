//go:build integration

package authoritystore_test

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore/contract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/dbparity"
	"github.com/stretchr/testify/require"
)

// TestDBParity_PostgresDirect is the canonical parity entry point for usage-authority on PostgreSQL Direct.
func TestDBParity_PostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)

	t.Run("Contract", func(t *testing.T) {
		contract.RunSuite(t, pgParityFactory{dsn: dsn, timeout: db.DefaultPostgresOpenMigrateTimeout})
	})

	t.Run("MigrationAndSchemaParity", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()

		bunDB := openPostgresAuthorityBun(t, dsn)
		defer bunDB.Close()
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
		require.NoError(t, authoritystore.Migrate(ctx, bunDB))
		var countAfter int
		require.NoError(t, bunDB.NewRaw("SELECT count(*) FROM bun_usage_authority_migrations").Scan(ctx, &countAfter))
		require.Equal(t, len(names), countAfter)
	})
}

type pgParityFactory struct {
	dsn     string
	timeout time.Duration
}

func (pgParityFactory) ParallelContract() bool { return false }

func (f pgParityFactory) Build(t *testing.T) app.StateStore {
	t.Helper()
	bunDB := openPostgresAuthorityBun(t, f.dsn)
	// Use unique store ID to avoid collision across parallel tests.
	storeID := nextPGStoreID("usage-parity")
	t.Cleanup(func() { cleanupAuthorityStore(t, f.dsn, storeID) })
	cfg := authoritystore.Config{StoreID: storeID, Backing: domain.BackingCapabilityAtomic, LimitRows: contract.SeededLimitRows(), Readiness: contract.SeededReadiness()}
	timeout := f.timeout
	if timeout <= 0 {
		timeout = db.DefaultPostgresOpenMigrateTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	store, err := authoritystore.NewDurable(ctx, bunDB, cfg)
	if err != nil {
		t.Fatalf("NewDurable postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
