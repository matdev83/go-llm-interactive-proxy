package authoritystore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

func reconcileReserveCommandInternal(ruleID string, value int64) app.ReserveCommand {
	key := domain.ReservationKey{LogicalRequestID: "seed-race", AttemptID: "seed-race", RuleID: ruleID, Sequence: 1}
	return app.ReserveCommand{
		ReservationKey: key, RuleID: ruleID, RuleType: string(domain.RuleKindQuota),
		Request:   domain.Amount{Unit: domain.AmountUnitRequests, Value: value},
		Authority: domain.AuthorityLevelAuthoritative, At: time.Now().UTC(), SourceKey: key.String(),
	}
}

func openSeedRaceDB(t *testing.T, path string) *bun.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	return bunDB
}

func TestLateConcurrentSeedCannotOverwriteReservedCapacity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "authority.db")
	row := controlplane.AccountingLimitStatusRow{
		RuleID: "seed-race", RuleType: string(domain.RuleKindQuota),
		Unit: string(domain.AmountUnitRequests), Limit: 100, Remaining: 100,
		Authority: controlplane.AccountingAuthoritySourceAuthoritative,
	}
	cfg := Config{StoreID: "seed-race", Backing: domain.BackingCapabilityAtomic, LimitRows: []controlplane.AccountingLimitStatusRow{row}}

	dbA := openSeedRaceDB(t, path)
	storeA := &DurableStore{db: dbA, c: newStoreCore(cfg)}
	if err := Migrate(ctx, dbA); err != nil {
		t.Fatal(err)
	}
	if err := storeA.seedAndFlush(ctx); err != nil {
		t.Fatal(err)
	}
	reserve := reconcileReserveCommandInternal("seed-race", 30)
	if result, err := storeA.Reserve(ctx, reserve); err != nil || !result.Applied {
		t.Fatalf("reserve = %#v, err=%v", result, err)
	}

	dbB := openSeedRaceDB(t, path)
	storeB := &DurableStore{db: dbB, c: newStoreCore(cfg)}
	if err := storeB.seedAndFlush(ctx); err != nil {
		t.Fatalf("late seed: %v", err)
	}
	page, err := storeA.LimitStatus(ctx, controlplane.AccountingLimitStatusQuery{RuleID: "seed-race", Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("limit status = %#v, err=%v", page.Items, err)
	}
	if page.Items[0].Reserved != 30 || page.Items[0].Remaining != 70 {
		t.Fatalf("late seed reset counters: %#v", page.Items[0])
	}
	_ = storeB.Close()
	_ = storeA.Close()
}
