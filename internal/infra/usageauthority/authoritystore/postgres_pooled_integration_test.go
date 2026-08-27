//go:build integration

package authoritystore_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore/contract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// Pooled-runtime contracts: admin Migrate/VerifySchema + runtime OpenStore without
// session affinity. Failures must cite runtime DDL / missing open-without-migrate
// when the pooler SQL guard trips.

func TestPostgresPooled_AdminBootstrapThenRuntimeDML(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	storeID := testkit.UniquePostgresStoreID("pg-pooled-auth")
	store, guard := openSharedPooledAuthorityStore(t, adminDSN, runtimeDSN, storeID)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := strictReserveCommandForPersistence()
	cmd.SourceKey = storeID + "-reserve"
	if _, err := store.Reserve(ctx, cmd); err != nil {
		t.Fatalf("runtime DML after admin close: %v", err)
	}
	guard.AssertNoViolations(t)
}

func TestPostgresPooled_AtomicReservationIdenticalReplayAcrossHandles(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	storeID := testkit.UniquePostgresStoreID("pg-pooled-replay")

	storeA, guardA := openOwnedPooledAuthorityStore(t, adminDSN, runtimeDSN, storeID)
	storeB, guardB := openOwnedPooledAuthorityStore(t, adminDSN, runtimeDSN, storeID)

	cmd := strictReserveCommandForPersistence()
	cmd.SourceKey = storeID + "-identical"
	start := make(chan struct{})
	type result struct {
		out app.ReserveResult
		err error
	}
	results := make(chan result, 2)
	workerCtx, workerCancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer workerCancel()
	var wg sync.WaitGroup
	for _, store := range []app.StateStore{storeA, storeB} {
		wg.Add(1)
		go func(store app.StateStore) {
			defer wg.Done()
			<-start
			out, err := store.Reserve(workerCtx, cmd)
			results <- result{out: out, err: err}
		}(store)
	}
	close(start)
	wg.Wait()
	close(results)

	applied := 0
	for res := range results {
		if res.err != nil {
			t.Fatalf("identical reserve under pooler: %v", res.err)
		}
		if res.out.Applied {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("applied=%d want exactly one identical replay winner", applied)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	page, err := storeA.LimitStatus(ctx, controlplane.AccountingLimitStatusQuery{
		RuleID: "rule-strict",
		Unit:   string(domain.AmountUnitRequests),
		Limit:  10,
	})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("LimitStatus=%#v err=%v", page, err)
	}
	if page.Items[0].Reserved != 60 {
		t.Fatalf("reserved=%d want 60", page.Items[0].Reserved)
	}
	guardA.AssertNoViolations(t)
	guardB.AssertNoViolations(t)
}

func TestPostgresPooled_RuntimeRejectsCapacityWithoutSessionSetup(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	storeID := testkit.UniquePostgresStoreID("pg-pooled-capacity")
	store, guard := openSharedPooledAuthorityStore(t, adminDSN, runtimeDSN, storeID)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := store.Reserve(ctx, strictReserveCommandForPersistence()); err != nil {
		t.Fatalf("fill: %v", err)
	}
	denied := strictReserveCommandForPersistence()
	denied.SourceKey = storeID + "-deny"
	denied.ReservationKey.AttemptID = "attempt-pooled-deny"
	denied.ReservationKey.Sequence = 9
	denied.Request = domain.Amount{Unit: domain.AmountUnitRequests, Value: 50}
	_, err := store.Reserve(ctx, denied)
	if err == nil || !errors.Is(err, app.ErrCapacityExceeded) {
		t.Fatalf("want capacity denial, got %#v err=%v", denied, err)
	}
	guard.AssertNoViolations(t)
}

// TestPostgresPooled_ConcurrentReserveCannotExceedLimit proves two OpenStore
// handles under a transaction pooler cannot over-commit a strict window
// (same invariant as TestPostgresAuthorityStore_ConcurrentReserveCannotExceedLimit).
func TestPostgresPooled_ConcurrentReserveCannotExceedLimit(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	storeID := testkit.UniquePostgresStoreID("pg-pooled-cross")

	storeA, guardA := openOwnedPooledAuthorityStore(t, adminDSN, runtimeDSN, storeID)
	storeB, guardB := openOwnedPooledAuthorityStore(t, adminDSN, runtimeDSN, storeID)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	reserveA := strictReserveCommandForPersistence()
	reserveA.SourceKey = storeID + "-reserve-a"
	reserveA.ReservationKey.AttemptID = "attempt-pooled-a"
	reserveA.ReservationKey.Sequence = 1
	reserveB := strictReserveCommandForPersistence()
	reserveB.SourceKey = storeID + "-reserve-b"
	reserveB.ReservationKey.AttemptID = "attempt-pooled-b"
	reserveB.ReservationKey.Sequence = 2

	type result struct {
		out app.ReserveResult
		err error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	run := func(store app.StateStore, cmd app.ReserveCommand) {
		defer wg.Done()
		<-start
		out, err := store.Reserve(ctx, cmd)
		results <- result{out: out, err: err}
	}
	go run(storeA, reserveA)
	go run(storeB, reserveB)
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	var failed error
	for res := range results {
		if res.err == nil {
			successes++
			continue
		}
		failed = res.err
	}
	if successes != 1 {
		t.Fatalf("concurrent reserve successes = %d, want 1 (no over-commit)", successes)
	}
	if failed == nil {
		t.Fatal("expected one concurrent reservation to fail")
	}
	if !errors.Is(failed, app.ErrReservationConflict) {
		t.Fatalf("failed reservation error = %v, want app.ErrReservationConflict", failed)
	}

	page, err := storeA.LimitStatus(ctx, controlplane.AccountingLimitStatusQuery{
		Common: controlplane.CommonFilters{
			Scope: controlplane.ScopeFilters{
				PrincipalID: scope.Known("principal-1"),
				TenantID:    scope.Known("tenant-1"),
			},
			BackendID: "backend-1",
			Model:     "model-1",
		},
		RuleID:     "rule-strict",
		Unit:       string(domain.AmountUnitRequests),
		Limit:      10,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("LimitStatus: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("LimitStatus items = %d, want 1", len(page.Items))
	}
	if page.Items[0].Reserved != 60 || page.Items[0].Remaining != 40 {
		t.Fatalf("cross-instance totals = reserved=%d remaining=%d, want reserved=60 remaining=40 (no over-commit)",
			page.Items[0].Reserved, page.Items[0].Remaining)
	}
	guardA.AssertNoViolations(t)
	guardB.AssertNoViolations(t)
}

func TestPostgresPooled_Contract(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	contract.RunSuite(t, pooledAuthorityFactory{adminDSN: adminDSN, runtimeDSN: runtimeDSN})
}

func TestPostgresPooled_VerifySchemaFailsWhenRequiredIndexMissing(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	pooledAuthTestMu.Lock()
	t.Cleanup(pooledAuthTestMu.Unlock)

	ensurePooledAuthoritySchema(t, adminDSN)
	runtime := sharedPooledAuthorityRuntime(t, runtimeDSN)

	admin := testkit.OpenPostgresBunForTest(t, adminDSN, 2)
	t.Cleanup(func() { _ = admin.Close() })
	t.Cleanup(func() {
		restoreCtx, restoreCancel := context.WithTimeout(context.Background(), pooledOpenTimeout)
		defer restoreCancel()
		_, _ = admin.ExecContext(restoreCtx, `CREATE INDEX IF NOT EXISTS usage_authority_limit_filters_lookup
			ON usage_authority_limit_filters(store_id, field_name, field_value, row_key)`)
	})

	mutateCtx, mutateCancel := context.WithTimeout(context.Background(), pooledOpenTimeout)
	defer mutateCancel()
	if _, err := admin.ExecContext(mutateCtx, `DROP INDEX IF EXISTS usage_authority_limit_filters_lookup`); err != nil {
		t.Fatalf("drop pooled authority index: %v", err)
	}

	verifyCtx, verifyCancel := context.WithTimeout(t.Context(), pooledOpenTimeout)
	defer verifyCancel()
	err := authoritystore.VerifySchema(verifyCtx, runtime)
	if err == nil {
		t.Fatal("expected verify_only schema validation to fail when the required index is missing")
	}
	if !strings.Contains(err.Error(), "usage_authority_limit_filters_lookup") {
		t.Fatalf("verify error %q does not mention the missing index", err)
	}
}

type pooledAuthorityFactory struct {
	adminDSN   string
	runtimeDSN string
}

func (f pooledAuthorityFactory) ParallelContract() bool { return false }

func (f pooledAuthorityFactory) Build(t *testing.T) app.StateStore {
	t.Helper()
	storeID := testkit.UniquePostgresStoreID("pg-pooled-contract")
	store, _ := openOwnedPooledAuthorityStore(t, f.adminDSN, f.runtimeDSN, storeID)
	return store
}
