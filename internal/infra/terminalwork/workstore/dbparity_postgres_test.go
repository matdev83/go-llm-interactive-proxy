//go:build integration

package workstore_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// TestDBParity_PostgresDirect is the canonical parity entry point for terminal-work on PostgreSQL Direct.
func TestDBParity_PostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	t.Run("CreateAndClose", func(t *testing.T) {
		t.Parallel()
		store := newPostgresWorkStore(t, dsn, "parity-pg-"+testkit.UniquePostgresStoreID("work"))
		_ = store
	})
}
