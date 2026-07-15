//go:build integration

package authoritystore

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/uptrace/bun"
)

type pgSeedLimitInsertBarrier struct {
	hit     chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *pgSeedLimitInsertBarrier) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if strings.Contains(strings.ToLower(event.Query), "insert into usage_authority_limit_rows") {
		b.once.Do(func() { close(b.hit) })
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	}
	return ctx
}

func (*pgSeedLimitInsertBarrier) AfterQuery(context.Context, *bun.QueryEvent) {}

func openPGSeedRaceDB(t *testing.T, dsn string) *bun.DB {
	t.Helper()
	return testkit.OpenPostgresBunForTest(t, dsn, 4)
}

func seedRaceAdminDSN(runtimeDSN string) string {
	if admin, ok := testkit.PostgresAdminDSN(); ok {
		return admin
	}
	return runtimeDSN
}

func TestPostgresSeedCannotEraseConcurrentReservation(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	storeID := testkit.UniquePostgresStoreID("seed-race")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, seedRaceAdminDSN(dsn), storeID, testkit.PostgresComponentAuthority)
	})
	row := controlplane.AccountingLimitStatusRow{
		RuleID: storeID, RuleType: string(domain.RuleKindQuota), Unit: string(domain.AmountUnitRequests),
		Limit: 100, Remaining: 100, Authority: controlplane.AccountingAuthoritySourceAuthoritative,
	}
	cfg := Config{StoreID: storeID, Backing: domain.BackingCapabilityAtomic, LimitRows: []controlplane.AccountingLimitStatusRow{row}, Readiness: domain.StatusFromBacking(domain.BackingCapabilityAtomic)}

	dbA := openPGSeedRaceDB(t, dsn)
	dbB := openPGSeedRaceDB(t, dsn)
	defer func() { _ = dbA.Close(); _ = dbB.Close() }()
	if err := Migrate(ctx, dbA); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Pre-create the stable coordination row. The seed transaction then has to
	// conflict on that row but must not overwrite it or any live limit counters.
	coreA := newStoreCore(cfg)
	readiness, err := json.Marshal(coreA.readiness())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbA.ExecContext(ctx, `INSERT INTO usage_authority_state(store_id, readiness_json, next_decision_seq) VALUES(?,?,?)`, cfg.StoreID, string(readiness), coreA.nextDecision); err != nil {
		t.Fatalf("seed coordination row: %v", err)
	}
	storeA := &DurableStore{db: dbA, c: coreA}
	storeB := &DurableStore{db: dbB, c: newStoreCore(cfg)}
	barrier := &pgSeedLimitInsertBarrier{hit: make(chan struct{}), release: make(chan struct{})}
	dbA.AddQueryHook(barrier)

	seedDone := make(chan error, 1)
	go func() { seedDone <- storeA.seedAndFlush(ctx) }()
	select {
	case <-barrier.hit:
	case <-time.After(5 * time.Second):
		close(barrier.release)
		t.Fatal("seed did not reach limit-row insert barrier")
	}

	reserve := reconcileReserveCommandInternal(storeID, 30)
	reserveDone := make(chan struct {
		out app.ReserveResult
		err error
	}, 1)
	go func() {
		out, err := storeB.Reserve(ctx, reserve)
		reserveDone <- struct {
			out app.ReserveResult
			err error
		}{out: out, err: err}
	}()
	select {
	case result := <-reserveDone:
		close(barrier.release)
		t.Fatalf("reservation completed before seed commit: %#v", result)
	case <-time.After(100 * time.Millisecond):
	}
	close(barrier.release)
	if err := <-seedDone; err != nil {
		t.Fatalf("seed: %v", err)
	}
	result := <-reserveDone
	if result.err != nil || !result.out.Applied {
		t.Fatalf("reservation after concurrent seed = %#v, err=%v", result.out, result.err)
	}
	page, err := storeB.LimitStatus(ctx, controlplane.AccountingLimitStatusQuery{RuleID: storeID, Limit: 10, Visibility: controlplane.VisibilityDefault})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("LimitStatus = %#v, err=%v", page.Items, err)
	}
	if page.Items[0].Reserved != 30 || page.Items[0].Remaining != 70 {
		t.Fatalf("seed race counters = reserved=%d remaining=%d, want 30/70", page.Items[0].Reserved, page.Items[0].Remaining)
	}
}
