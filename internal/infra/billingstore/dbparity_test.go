package billingstore

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/dbparity"
)

// TestDBParity_SQLite is the canonical parity entry point for billing on SQLite.
func TestDBParity_SQLite(t *testing.T) {
	t.Parallel()

	t.Run("Contract", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteTestStore(t)
		runBillingStoreContract(t, store, "contract-parity-sqlite")
	})

	t.Run("CreateAndVerifySchema", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteTestStore(t)
		if err := VerifySchema(context.Background(), store.db); err != nil {
			t.Fatalf("VerifySchema sqlite: %v", err)
		}
	})

	t.Run("MigrationHistoryParity", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		store := newSQLiteTestStore(t)

		_, thisFile, _, ok := runtime.Caller(0)
		if !ok {
			t.Fatal("runtime.Caller failed")
		}
		discovered, err := dbparity.DiscoverMigrations(filepath.Dir(thisFile))
		if err != nil {
			t.Fatalf("DiscoverMigrations: %v", err)
		}
		if len(discovered) == 0 {
			t.Fatal("no migrations discovered")
		}

		var names []string
		rows, err := store.db.QueryContext(ctx, "SELECT name FROM bun_billing_migrations")
		if err != nil {
			t.Fatalf("query bun_billing_migrations: %v", err)
		}
		defer rows.Close()
		recorded := make(map[string]bool)
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("scan migration name: %v", err)
			}
			names = append(names, name)
			id := name
			if len(name) >= 14 {
				id = name[:14]
			}
			recorded[id] = true
		}
		var requiredIDs []string
		for _, m := range discovered {
			// 20260824000000 is immutable historical source only; fresh schemas intentionally
			// omit its registration because forward cutover 20260829000000 handles retirement.
			if m.ID == UsageAppendOutboxMigrationName {
				continue
			}
			requiredIDs = append(requiredIDs, m.ID)
		}
		if err := dbparity.AssertMigrationHistoryIDs(requiredIDs, recorded); err != nil {
			t.Fatalf("AssertMigrationHistoryIDs: %v", err)
		}

		// Verify migration rerun idempotency
		if err := Migrate(ctx, store.db); err != nil {
			t.Fatalf("Migrate rerun: %v", err)
		}
		var countAfter int
		if err := store.db.NewRaw("SELECT count(*) FROM bun_billing_migrations").Scan(ctx, &countAfter); err != nil {
			t.Fatalf("count bun_billing_migrations after rerun: %v", err)
		}
		if countAfter != len(names) {
			t.Fatalf("migration count after rerun = %d, want %d", countAfter, len(names))
		}
	})
}
