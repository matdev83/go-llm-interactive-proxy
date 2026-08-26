//go:build integration

package authoritystore_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore/contract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// TestDBParity_PostgresDirect is the canonical parity entry point for usage-authority on PostgreSQL Direct.
func TestDBParity_PostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	contract.RunSuite(t, pgParityFactory{dsn: dsn})
}

type pgParityFactory struct {
	dsn string
}

func (f pgParityFactory) Build(t *testing.T) app.StateStore {
	t.Helper()
	bunDB := openPostgresAuthorityBun(t, f.dsn)
	// Use unique store ID to avoid collision across parallel tests.
	storeID := nextPGStoreID("usage-parity")
	t.Cleanup(func() { cleanupAuthorityStore(t, f.dsn, storeID) })
	cfg := authoritystore.Config{StoreID: storeID, Backing: domain.BackingCapabilityAtomic, LimitRows: contract.SeededLimitRows(), Readiness: contract.SeededReadiness()}
	store, err := authoritystore.NewDurable(context.Background(), bunDB, cfg)
	if err != nil {
		t.Fatalf("NewDurable postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
