//go:build integration

package journalstore_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/uptrace/bun"
	"go.uber.org/goleak"
)

const pooledJournalOpenTimeout = 2 * time.Minute

var (
	pooledJournalSchemaOnce sync.Once
	pooledJournalSchemaErr  error

	pooledJournalRuntimeOnce sync.Once
	pooledJournalRuntimeDB   *bun.DB
	pooledJournalRuntimeErr  error
	pooledJournalSharedGuard = testkit.NewRuntimeSQLGuard()
	// pooledJournalTestMu serializes shared-pool tests so Reset/Assert cannot cross-talk
	// if a test accidentally calls t.Parallel().
	pooledJournalTestMu sync.Mutex
)

func TestMain(m *testing.M) {
	code := m.Run()
	if pooledJournalRuntimeDB != nil {
		_ = pooledJournalRuntimeDB.Close()
	}
	if code == 0 {
		if err := goleak.Find(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "goleak: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

func ensurePooledJournalSchema(t *testing.T, adminDSN string) {
	t.Helper()
	pooledJournalSchemaOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), pooledJournalOpenTimeout)
		defer cancel()
		admin, err := testkit.OpenPostgresBun(adminDSN, 2)
		if err != nil {
			pooledJournalSchemaErr = err
			return
		}
		bootstrap, err := journalstore.NewDurableStore(ctx, admin, journalstore.DurableConfig{
			StoreID: testkit.UniquePostgresStoreID("pg-journal-schema"),
		})
		if err != nil {
			_ = admin.Close()
			pooledJournalSchemaErr = err
			return
		}
		pooledJournalSchemaErr = bootstrap.Close()
	})
	if pooledJournalSchemaErr != nil {
		t.Fatalf("package admin schema bootstrap: %v", pooledJournalSchemaErr)
	}
}

func sharedPooledJournalRuntime(t *testing.T, runtimeDSN string) *bun.DB {
	t.Helper()
	pooledJournalRuntimeOnce.Do(func() {
		db, err := testkit.OpenPostgresBun(runtimeDSN, 2)
		if err != nil {
			pooledJournalRuntimeErr = err
			return
		}
		db.AddQueryHook(pooledJournalSharedGuard)
		pooledJournalRuntimeDB = db
	})
	if pooledJournalRuntimeErr != nil {
		t.Fatalf("package shared runtime pool: %v", pooledJournalRuntimeErr)
	}
	return pooledJournalRuntimeDB
}

func openSharedPooledJournalStore(t *testing.T, adminDSN, runtimeDSN, storeID string) (*journalstore.DurableStore, *testkit.RuntimeSQLGuard) {
	t.Helper()
	pooledJournalTestMu.Lock()
	t.Cleanup(pooledJournalTestMu.Unlock)
	ensurePooledJournalSchema(t, adminDSN)
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSN, storeID, testkit.PostgresComponentJournal)
	})

	pooledJournalSharedGuard.Reset()
	runtime := sharedPooledJournalRuntime(t, runtimeDSN)
	ctx, cancel := context.WithTimeout(t.Context(), pooledJournalOpenTimeout)
	defer cancel()
	if err := journalstore.VerifySchema(ctx, runtime); err != nil {
		t.Fatalf("verify runtime schema: %v", err)
	}
	store, err := journalstore.OpenStore(ctx, runtime, journalstore.DurableConfig{StoreID: storeID})
	if err != nil {
		t.Fatalf("open pooled runtime store: %v", err)
	}
	pooledJournalSharedGuard.AssertNoViolations(t)
	return store, pooledJournalSharedGuard
}
