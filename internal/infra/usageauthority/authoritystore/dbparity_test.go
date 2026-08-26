package authoritystore_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore/contract"
)

// TestDBParity_SQLite is the canonical parity entry point for usage-authority on SQLite.
func TestDBParity_SQLite(t *testing.T) {
	t.Parallel()
	contract.RunSuite(t, sqliteParityFactory{})
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
