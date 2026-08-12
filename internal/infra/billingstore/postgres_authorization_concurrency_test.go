//go:build integration

package billingstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func TestPostgresConcurrentAuthorizationsNeverOverspend(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	firstDB, err := db.OpenPostgresBun(ctx, dsn, db.PoolSettings{MaxOpenConns: 8, MaxIdleConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewDurableStore(ctx, firstDB, Config{StoreID: "concurrency-first"})
	if err != nil {
		_ = firstDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })

	secondDB, err := db.OpenPostgresBun(ctx, dsn, db.PoolSettings{MaxOpenConns: 8, MaxIdleConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenStore(ctx, secondDB, Config{StoreID: "concurrency-second"})
	if err != nil {
		_ = secondDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondDB.Close() })

	runConcurrentAuthorizationContract(t, first, second, fmt.Sprintf("concurrent-postgres-%d", time.Now().UnixNano()))
}
