//go:build integration

package authoritystore_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore/contract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/uptrace/bun"
	"go.uber.org/goleak"
)

const pooledOpenTimeout = 2 * time.Minute

var (
	pooledAuthSchemaOnce sync.Once
	pooledAuthSchemaErr  error

	pooledAuthRuntimeOnce sync.Once
	pooledAuthRuntimeDB   *bun.DB
	pooledAuthRuntimeErr  error
	pooledAuthSharedGuard = testkit.NewRuntimeSQLGuard()
	// pooledAuthTestMu serializes shared-pool tests so Reset/Assert cannot cross-talk
	// if a test accidentally calls t.Parallel().
	pooledAuthTestMu sync.Mutex
)

func TestMain(m *testing.M) {
	code := m.Run()
	if pooledAuthRuntimeDB != nil {
		_ = pooledAuthRuntimeDB.Close()
	}
	if code == 0 {
		if err := goleak.Find(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "goleak: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

func ensurePooledAuthoritySchema(t *testing.T, adminDSN string) {
	t.Helper()
	pooledAuthSchemaOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), pooledOpenTimeout)
		defer cancel()
		admin, err := testkit.OpenPostgresBun(adminDSN, 2)
		if err != nil {
			pooledAuthSchemaErr = err
			return
		}
		defer func() { _ = admin.Close() }()
		pooledAuthSchemaErr = authoritystore.Migrate(ctx, admin)
	})
	if pooledAuthSchemaErr != nil {
		t.Fatalf("package admin schema bootstrap: %v", pooledAuthSchemaErr)
	}
}

func sharedPooledAuthorityRuntime(t *testing.T, runtimeDSN string) *bun.DB {
	t.Helper()
	pooledAuthRuntimeOnce.Do(func() {
		db, err := testkit.OpenPostgresBun(runtimeDSN, 4)
		if err != nil {
			pooledAuthRuntimeErr = err
			return
		}
		db.AddQueryHook(pooledAuthSharedGuard)
		pooledAuthRuntimeDB = db
	})
	if pooledAuthRuntimeErr != nil {
		t.Fatalf("package shared runtime pool: %v", pooledAuthRuntimeErr)
	}
	return pooledAuthRuntimeDB
}

func authoritySeed(storeID string) authoritystore.Config {
	return authoritystore.Config{
		StoreID:   storeID,
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: contract.SeededLimitRows(),
		Readiness: contract.SeededReadiness(),
	}
}

// openSharedPooledAuthorityStore opens a store on the package-owned shared runtime
// pool. DurableStore.Close closes the injected handle, so callers must NOT Close
// the store when using the shared pool (Phase 3 ownership split).
func openSharedPooledAuthorityStore(t *testing.T, adminDSN, runtimeDSN, storeID string) (app.StateStore, *testkit.RuntimeSQLGuard) {
	t.Helper()
	pooledAuthTestMu.Lock()
	t.Cleanup(pooledAuthTestMu.Unlock)
	ensurePooledAuthoritySchema(t, adminDSN)
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSN, storeID, testkit.PostgresComponentAuthority)
	})

	pooledAuthSharedGuard.Reset()
	runtime := sharedPooledAuthorityRuntime(t, runtimeDSN)

	ctx, cancel := context.WithTimeout(t.Context(), pooledOpenTimeout)
	defer cancel()
	if err := authoritystore.VerifySchema(ctx, runtime); err != nil {
		t.Fatalf("verify runtime schema: %v", err)
	}
	store, err := authoritystore.OpenStore(ctx, runtime, authoritySeed(storeID))
	if err != nil {
		t.Fatalf("open pooled runtime store: %v", err)
	}
	pooledAuthSharedGuard.AssertNoViolations(t)
	return store, pooledAuthSharedGuard
}

// openOwnedPooledAuthorityStore opens a dedicated runtime pool owned by the store.
// Use for cross-instance proofs that require exactly N handles.
func openOwnedPooledAuthorityStore(t *testing.T, adminDSN, runtimeDSN, storeID string) (app.StateStore, *testkit.RuntimeSQLGuard) {
	t.Helper()
	ensurePooledAuthoritySchema(t, adminDSN)
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSN, storeID, testkit.PostgresComponentAuthority)
	})

	guard := testkit.NewRuntimeSQLGuard()
	runtime := testkit.OpenPostgresBunForTest(t, runtimeDSN, 4)
	runtime.AddQueryHook(guard)

	ctx, cancel := context.WithTimeout(t.Context(), pooledOpenTimeout)
	defer cancel()
	if err := authoritystore.VerifySchema(ctx, runtime); err != nil {
		_ = runtime.Close()
		t.Fatalf("verify runtime schema: %v", err)
	}
	store, err := authoritystore.OpenStore(ctx, runtime, authoritySeed(storeID))
	if err != nil {
		_ = runtime.Close()
		t.Fatalf("open pooled runtime store: %v", err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Fatalf("close pooled runtime: %v", err)
		}
		if err := runtime.PingContext(context.Background()); err == nil {
			t.Fatal("pooled runtime should be closed")
		}
	})
	guard.AssertNoViolations(t)
	return store, guard
}
