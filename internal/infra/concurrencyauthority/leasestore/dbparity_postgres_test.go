//go:build integration

package leasestore_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// TestDBParity_PostgresDirect is the canonical parity entry point for concurrency-authority on PostgreSQL Direct.
func TestDBParity_PostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	runLeaseParitySuite(t, func(t *testing.T) *leaseStoreFactory {
		t.Helper()
		storeID := "parity-pg-" + testkit.UniquePostgresStoreID("lease")
		store := newPostgresStore(t, dsn, storeID)
		t.Cleanup(func() { testkit.CleanupPostgresStoreByID(t, dsn, storeID, testkit.PostgresComponentAuthority) })
		return &leaseStoreFactory{store: store}
	})
}
