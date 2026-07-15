//go:build integration

package authoritystore_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore/contract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func TestAuthorityStorePostgresDifferentialSequence(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	memory := authoritystore.NewMemory(authoritystore.Config{Backing: domain.BackingCapabilityAtomic, LimitRows: contract.SeededLimitRows(), Readiness: contract.SeededReadiness()})
	t.Cleanup(func() { _ = memory.Close() })

	storeID := nextPGStoreID("differential-pg")
	cleanupAuthorityStore(t, dsn, storeID)
	bunDB := openPostgresAuthorityBun(t, dsn)
	store, err := authoritystore.NewDurable(context.Background(), bunDB, authoritystore.Config{
		StoreID: storeID,
		Backing: domain.BackingCapabilityAtomic, LimitRows: contract.SeededLimitRows(), Readiness: contract.SeededReadiness(),
	})
	if err != nil {
		_ = bunDB.Close()
		t.Fatalf("NewDurable PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	runDifferentialSequence(t, "memory", memory)
	runDifferentialSequence(t, "postgres", store)
	assertEquivalentState(t, memory, store)
}
