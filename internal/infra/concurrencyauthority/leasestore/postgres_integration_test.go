//go:build integration

package leasestore_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/concurrencyauthority/leasestore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

var (
	pgBuildMu   sync.Mutex
	pgSchemaSeq uint64
)

func TestPostgresStore_FiveSlotAcrossTwoInstances(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	schemaDSN := openLeaseSchemaDSN(t, dsn)
	a := newPostgresStore(t, schemaDSN, "pg-lease")
	b := newPostgresStore(t, schemaDSN, "pg-lease")
	runFiveSlotContract(t, a, b)
}

func TestPostgresStore_ReadinessDistributedStrict(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	store := newPostgresStore(t, openLeaseSchemaDSN(t, dsn), "pg-ready")
	ready, err := store.CheckReadiness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != domain.ReadinessStateReady {
		t.Fatalf("state=%s want ready", ready.State)
	}
	if !strings.Contains(strings.ToLower(ready.Reason), "postgres") &&
		!strings.Contains(strings.ToLower(ready.Reason), "distributed") {
		t.Fatalf("expected distributed/postgres readiness reason, got %q", ready.Reason)
	}
}

func TestPostgresStore_ConcurrentReleaseRenew_NoResurrection(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	store := newPostgresStore(t, openLeaseSchemaDSN(t, dsn), "pg-cas-race")
	runConcurrentReleaseRenewNoResurrection(t, store)
}

func openLeaseSchemaDSN(t *testing.T, dsn string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	pool := db.PoolSettings{
		MaxOpenConns:    2,
		MaxIdleConns:    1,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	}
	pgBuildMu.Lock()
	defer pgBuildMu.Unlock()
	seq := atomic.AddUint64(&pgSchemaSeq, 1)
	schema := fmt.Sprintf("lease_test_%d_%d", time.Now().UnixNano(), seq)
	bootstrap, err := db.OpenPostgresBun(ctx, dsn, pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = bootstrap.Close()
		t.Fatal(err)
	}
	_ = bootstrap.Close()
	schemaDSN := dsn
	if !strings.Contains(dsn, "search_path=") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		schemaDSN = dsn + sep + "search_path=" + schema
	}
	return schemaDSN
}

func newPostgresStore(t *testing.T, schemaDSN, storeID string) *leasestore.DurableStore {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	bunDB, err := db.OpenPostgresBun(ctx, schemaDSN, db.PoolSettings{
		MaxOpenConns:    poolCfg.MaxOpenConns,
		MaxIdleConns:    poolCfg.MaxIdleConns,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := leasestore.NewDurable(ctx, bunDB, leasestore.DurableConfig{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
