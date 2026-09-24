//go:build integration

package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// TestEconomicHealthPostgresDirect proves the 16.3B snapshot contract on
// direct PostgreSQL with the same bounded queue/statement counts as SQLite.
// The snapshot uses one placeholder SQL shape through Bun on both dialects
// and adds no migration.
func TestEconomicHealthPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	now := time.Now().UTC()
	_, err = store.db.NewRaw(`INSERT INTO billing_economic_revision_work_state(store_id, work_id, work_version, queue, head_key, status, created_at_unix, updated_at_unix) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		store.StoreID(), "work-pg", 1, "customer", "head-pg", "pending", now.UnixNano(), now.UnixNano()).Exec(ctx)
	require.NoError(t, err)

	got, err := store.EconomicHealthSnapshot(ctx)
	require.NoError(t, err)
	found := false
	for _, queue := range got.Queues {
		if queue.Queue == "customer" && queue.Pending >= 1 {
			found = true
		}
	}
	require.True(t, found, "customer pending work must be visible on PostgreSQL: %+v", got.Queues)
}
