//go:build integration

package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// TestDBParity_PostgresDirect is the canonical parity entry point for billing on PostgreSQL Direct.
func TestDBParity_PostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	t.Run("CreateAndVerifySchema", func(t *testing.T) {
		t.Parallel()
		bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
		store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: "parity-billing"})
		if err != nil {
			t.Fatalf("NewDurableStore postgres: %v", err)
		}
		defer func() { _ = store.Close() }()
		if err := VerifySchema(context.Background(), store.db); err != nil {
			t.Fatalf("VerifySchema postgres: %v", err)
		}
	})
}
