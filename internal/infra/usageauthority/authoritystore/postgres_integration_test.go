//go:build integration

package authoritystore_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore/contract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/uptrace/bun"
)

var pgAuthorityStoreSeq atomic.Int64
var pgAuthorityRunID = time.Now().UnixNano()

func nextPGStoreID(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, pgAuthorityRunID, pgAuthorityStoreSeq.Add(1))
}

type pgUsageFactReadBarrier struct {
	arrived atomic.Int64
	release chan struct{}
	once    sync.Once
}

func newPGUsageFactReadBarrier() *pgUsageFactReadBarrier {
	return &pgUsageFactReadBarrier{release: make(chan struct{})}
}

func (b *pgUsageFactReadBarrier) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	query := strings.ToLower(event.Query)
	if strings.Contains(query, "select record_json from usage_authority_unreserved_usage_facts") {
		if arrived := b.arrived.Add(1); arrived <= 2 {
			if arrived == 2 {
				b.once.Do(func() { close(b.release) })
			}
			select {
			case <-b.release:
			case <-ctx.Done():
			}
		}
	}
	return ctx
}

func (*pgUsageFactReadBarrier) AfterQuery(context.Context, *bun.QueryEvent) {}

type pgLimitRowReadBarrier struct {
	arrived atomic.Int64
	release chan struct{}
	once    sync.Once
}

func newPGLimitRowReadBarrier() *pgLimitRowReadBarrier {
	return &pgLimitRowReadBarrier{release: make(chan struct{})}
}

func (b *pgLimitRowReadBarrier) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	query := strings.ToLower(event.Query)
	if strings.Contains(query, "select row_json from usage_authority_limit_rows") {
		if arrived := b.arrived.Add(1); arrived <= 2 {
			if arrived == 2 {
				b.once.Do(func() { close(b.release) })
			}
			select {
			case <-b.release:
			case <-ctx.Done():
			}
		}
	}
	return ctx
}

func (*pgLimitRowReadBarrier) AfterQuery(context.Context, *bun.QueryEvent) {}

// openPostgresAuthorityBun opens a Bun-backed PostgreSQL database for the
// authority store integration tests. The returned *bun.DB owns the underlying
// *sql.DB (Close closes both). Each call opens a fresh pool so concurrent
// subtests do not share a connection pool.
func openPostgresAuthorityBun(t *testing.T, dsn string) *bun.DB {
	t.Helper()
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	bunDB, err := db.OpenPostgresBun(ctx, dsn, db.PoolSettings{
		MaxOpenConns:    poolCfg.MaxOpenConns,
		MaxIdleConns:    poolCfg.MaxIdleConns,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	return bunDB
}

// pgFactory builds a fresh DurableStore against a shared PostgreSQL database,
// namespaced by a unique StoreID per Build so contract subtests do not collide
// on the seeded rows.
type pgFactory struct {
	dsn string
}

func (f pgFactory) Build(t *testing.T) app.StateStore {
	t.Helper()
	storeID := nextPGStoreID("pg-authority")
	bunDB := openPostgresAuthorityBun(t, f.dsn)
	store, err := authoritystore.NewDurable(context.Background(), bunDB, authoritystore.Config{
		StoreID:   storeID,
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: contract.SeededLimitRows(),
		Readiness: contract.SeededReadiness(),
	})
	if err != nil {
		_ = bunDB.Close()
		t.Fatalf("NewDurable: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestPostgresAuthorityStore_Contract runs the shared authority-store contract
// suite (including the advisory ApplyUsage subtest) against a Bun-backed
// PostgreSQL database when LIP_TEST_POSTGRES_DSN (or legacy
// LIP_MANAGED_POSTGRES_DSN) is set. CI must set LIP_TEST_POSTGRES_DSN for this
// test to run; otherwise it skips cleanly.
func TestPostgresAuthorityStore_Contract(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	contract.RunSuite(t, pgFactory{dsn: dsn})
}

// TestPostgresAuthorityStore_ConcurrentReserveCannotExceedLimit proves two
// DurableStore instances sharing one PostgreSQL database (separate in-memory
// projections, like two proxy processes) cannot over-commit a strict window
// (requirement 11.1). The durable flush locks the affected limit rows with
// SELECT ... FOR UPDATE before the capacity check, so the second instance's
// transaction blocks until the first commits and then sees the committed
// reservation; the conditional UPDATE on the pre-image row_json backstops the
// lock by detecting a lost update and returning app.ErrReservationConflict.
func TestPostgresAuthorityStore_ConcurrentReserveCannotExceedLimit(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	storeID := nextPGStoreID("pg-cross")
	seed := authoritystore.Config{
		StoreID:   storeID,
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: contract.SeededLimitRows(),
		Readiness: contract.SeededReadiness(),
	}

	// Instance A opens first and seeds the live rows.
	dbA := openPostgresAuthorityBun(t, dsn)
	durableA, err := authoritystore.NewDurable(context.Background(), dbA, seed)
	if err != nil {
		_ = dbA.Close()
		t.Fatalf("NewDurable A: %v", err)
	}
	defer func() { _ = durableA.Close() }()

	// Instance B opens against the same database and hydrates from the seeded
	// rows. Its in-memory projection is independent of instance A's.
	dbB := openPostgresAuthorityBun(t, dsn)
	durableB, err := authoritystore.NewDurable(context.Background(), dbB, seed)
	if err != nil {
		_ = dbB.Close()
		t.Fatalf("NewDurable B: %v", err)
	}
	defer func() { _ = durableB.Close() }()

	ctx := context.Background()
	reserveA := strictReserveCommandForPersistence()
	reserveA.SourceKey = "pg-cross-reserve-a"
	reserveA.ReservationKey.AttemptID = "attempt-pg-a"
	reserveA.ReservationKey.Sequence = 1
	reserveB := strictReserveCommandForPersistence()
	reserveB.SourceKey = "pg-cross-reserve-b"
	reserveB.ReservationKey.AttemptID = "attempt-pg-b"
	reserveB.ReservationKey.Sequence = 2

	type result struct {
		out app.ReserveResult
		err error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	run := func(name string, store app.StateStore, cmd app.ReserveCommand) {
		defer wg.Done()
		<-start
		out, err := store.Reserve(ctx, cmd)
		results <- result{out: out, err: err}
	}
	go run("a", durableA, reserveA)
	go run("b", durableB, reserveB)
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
		t.Fatalf("expected one concurrent reservation to fail")
	}
	if !errors.Is(failed, app.ErrReservationConflict) {
		t.Fatalf("failed reservation error = %v, want app.ErrReservationConflict", failed)
	}

	// The committed DB state must reflect a single reservation (reserved=60,
	// remaining=40), not 120. Query through instance A, whose projection is
	// kept consistent with the committed state.
	page, err := durableA.LimitStatus(ctx, controlplane.AccountingLimitStatusQuery{
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
}

func TestPostgresAuthorityStore_ConcurrentIdenticalReserveIsIdempotent(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	storeID := nextPGStoreID("pg-identical")
	seed := authoritystore.Config{StoreID: storeID, Backing: domain.BackingCapabilityAtomic, LimitRows: contract.SeededLimitRows(), Readiness: contract.SeededReadiness()}
	dbA := openPostgresAuthorityBun(t, dsn)
	storeA, err := authoritystore.NewDurable(context.Background(), dbA, seed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storeA.Close() }()
	dbB := openPostgresAuthorityBun(t, dsn)
	storeB, err := authoritystore.NewDurable(context.Background(), dbB, seed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storeB.Close() }()

	cmd := strictReserveCommandForPersistence()
	start := make(chan struct{})
	type result struct {
		out app.ReserveResult
		err error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, store := range []app.StateStore{storeA, storeB} {
		wg.Add(1)
		go func(store app.StateStore) {
			defer wg.Done()
			<-start
			out, err := store.Reserve(context.Background(), cmd)
			results <- result{out: out, err: err}
		}(store)
	}
	close(start)
	wg.Wait()
	close(results)
	applied := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("identical reserve: %v", result.err)
		}
		if result.out.Applied {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("applied results = %d, want exactly one", applied)
	}
	page, err := storeA.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{RuleID: "rule-strict", Unit: string(domain.AmountUnitRequests), Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("LimitStatus = %#v, err=%v", page, err)
	}
	if page.Items[0].Reserved != 60 {
		t.Fatalf("reserved = %d, want one reservation (60)", page.Items[0].Reserved)
	}
}

func TestPostgresAuthorityStore_RepeatedCapacityDenialIsIdempotent(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	storeID := nextPGStoreID("pg-capacity-denial")
	seed := authoritystore.Config{StoreID: storeID, Backing: domain.BackingCapabilityAtomic, LimitRows: contract.SeededLimitRows(), Readiness: contract.SeededReadiness()}
	db := openPostgresAuthorityBun(t, dsn)
	store, err := authoritystore.NewDurable(context.Background(), db, seed)
	if err != nil {
		_ = db.Close()
		t.Fatalf("NewDurable: %v", err)
	}
	defer func() { _ = store.Close() }()

	if _, err := store.Reserve(context.Background(), strictReserveCommandForPersistence()); err != nil {
		t.Fatalf("fill reserve: %v", err)
	}
	denied := strictReserveCommandForPersistence()
	denied.SourceKey = "pg-capacity-denial-replay"
	denied.ReservationKey.AttemptID = "attempt-pg-capacity-denial"
	denied.ReservationKey.Sequence = 8
	denied.Request = domain.Amount{Unit: domain.AmountUnitRequests, Value: 50}
	for attempt := 1; attempt <= 2; attempt++ {
		out, err := store.Reserve(context.Background(), denied)
		if err == nil || !errors.Is(err, app.ErrCapacityExceeded) || errors.Is(err, app.ErrUnavailable) {
			t.Fatalf("attempt %d denial = %#v, err=%v; want typed capacity denial", attempt, out, err)
		}
	}
	page, err := store.DecisionHistory(context.Background(), controlplane.AccountingDecisionQuery{RuleID: "rule-strict", Common: controlplane.CommonFilters{ReasonCode: "quota_exceeded"}, Limit: 20, Visibility: controlplane.VisibilityDefault})
	if err != nil {
		t.Fatalf("DecisionHistory: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("capacity denial decisions = %d, want one durable replay marker", len(page.Items))
	}
}

func TestPostgresAuthorityStore_ConcurrentUsageFactsConvergeToFinal(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rule := advisoryQuotaRule("pg-usage-fact")
	rows, err := authoritystore.LimitRowsFromRules([]domain.Rule{rule}, at)
	if err != nil {
		t.Fatal(err)
	}
	seed := authoritystore.Config{StoreID: nextPGStoreID("pg-fact"), Backing: domain.BackingCapabilityAtomic, LimitRows: rows, Readiness: contract.SeededReadiness()}
	barrier := newPGUsageFactReadBarrier()
	dbA := openPostgresAuthorityBun(t, dsn)
	dbA.AddQueryHook(barrier)
	storeA, err := authoritystore.NewDurable(context.Background(), dbA, seed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storeA.Close() }()
	dbB := openPostgresAuthorityBun(t, dsn)
	dbB.AddQueryHook(barrier)
	storeB, err := authoritystore.NewDurable(context.Background(), dbB, seed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storeB.Close() }()

	partial := applyUsageCmd(rule.ID, advisoryQuotaDimensions(), domain.PreflightUsage{}, domain.Amount{Unit: domain.AmountUnitRequests, Value: 3}, domain.Amount{}, at.Add(time.Minute), "pg-shared-fact")
	partial.Authority = domain.AuthorityLevelEstimated
	partial.Kind = app.SettlementKindPartial
	final := partial
	final.RequestCount.Value = 5
	final.Kind = app.SettlementKindFinal
	type result struct{ err error }
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, call := range []struct {
		store app.StateStore
		cmd   app.ApplyUsageCommand
	}{{storeA, partial}, {storeB, final}} {
		wg.Add(1)
		go func(call struct {
			store app.StateStore
			cmd   app.ApplyUsageCommand
		},
		) {
			defer wg.Done()
			_, err := call.store.ApplyUsage(context.Background(), call.cmd)
			results <- result{err: err}
		}(call)
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result.err != nil {
			t.Fatalf("ApplyUsage: %v", result.err)
		}
	}
	page, err := storeA.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{RuleID: rule.ID, Unit: string(domain.AmountUnitRequests), Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("LimitStatus = %#v, err=%v", page, err)
	}
	if page.Items[0].Consumed != 5 {
		t.Fatalf("Consumed = %d, want final fact 5 without double count", page.Items[0].Consumed)
	}
}

func TestPostgresAuthorityStore_ConcurrentRolloverReservationsRetry(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	anchor := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	rule := windowedQuotaRule("pg-rollover", anchor, time.Hour)
	rows, err := authoritystore.LimitRowsFromRules([]domain.Rule{rule}, anchor.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	seed := authoritystore.Config{
		StoreID:     nextPGStoreID("pg-rollover"),
		Backing:     domain.BackingCapabilityAtomic,
		LimitRows:   rows,
		RuleWindows: map[string]domain.WindowSpec{rule.ID: rule.Window},
		Readiness:   contract.SeededReadiness(),
	}
	dbA := openPostgresAuthorityBun(t, dsn)
	storeA, err := authoritystore.NewDurable(context.Background(), dbA, seed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storeA.Close() }()
	dbB := openPostgresAuthorityBun(t, dsn)
	storeB, err := authoritystore.NewDurable(context.Background(), dbB, seed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storeB.Close() }()
	barrier := newPGLimitRowReadBarrier()
	dbA.AddQueryHook(barrier)
	dbB.AddQueryHook(barrier)

	at := anchor.Add(90 * time.Minute)
	cmdA := reserveCmd(rule.ID, "quota", windowedDimensions(), domain.Amount{Unit: domain.AmountUnitRequests, Value: 30}, at, "pg-rollover-a")
	cmdA.ReservationKey.AttemptID = "pg-rollover-a"
	cmdB := reserveCmd(rule.ID, "quota", windowedDimensions(), domain.Amount{Unit: domain.AmountUnitRequests, Value: 30}, at, "pg-rollover-b")
	cmdB.ReservationKey.AttemptID = "pg-rollover-b"
	type result struct{ err error }
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, call := range []struct {
		store app.StateStore
		cmd   app.ReserveCommand
	}{{storeA, cmdA}, {storeB, cmdB}} {
		wg.Add(1)
		go func(call struct {
			store app.StateStore
			cmd   app.ReserveCommand
		},
		) {
			defer wg.Done()
			_, err := call.store.Reserve(context.Background(), call.cmd)
			results <- result{err: err}
		}(call)
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result.err != nil {
			t.Fatalf("rollover reserve: %v", result.err)
		}
	}
	row, configured, err := storeA.ActiveLimit(context.Background(), app.ActiveLimitQuery{RuleID: rule.ID, Dimensions: windowedDimensions(), At: at})
	if err != nil || !configured {
		t.Fatalf("ActiveLimit configured=%v err=%v", configured, err)
	}
	if row.Reserved != 60 || row.Remaining != 40 {
		t.Fatalf("rollover row reserved=%d remaining=%d, want 60/40", row.Reserved, row.Remaining)
	}
}
