//go:build integration

package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// review583PostgresStore opens one isolated PostgreSQL schema and a durable
// billing store whose StoreID matches ref83intObservation's subject/correlation
// ("ref83"). openIsolatedPostgresBun registers its own schema cleanup, and the
// store owns the bun handle.
func review583PostgresStore(t *testing.T) *DurableStore {
	t.Helper()
	dsn := testkit.SkipUnlessPostgres(t)
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: ref83intStoreID})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestReview583SettlementPostgresWhenConfigured runs the same P1-A/P1-B
// settlement proof on configured direct PostgreSQL. It skips unless
// LIP_REQUIRE_POSTGRES=1 and a DSN is configured; the SQLite suite is
// authoritative when unavailable.
func TestReview583SettlementPostgresWhenConfigured(t *testing.T) {
	t.Parallel()
	review583RunSettlementSuite(t, review583PostgresStore)
}
