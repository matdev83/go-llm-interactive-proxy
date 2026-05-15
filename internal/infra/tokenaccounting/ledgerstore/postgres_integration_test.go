//go:build integration

package ledgerstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestPostgresLedgerStore_recordsRoundTrip(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	bunDB, err := db.OpenPostgresBun(ctx, dsn, db.PoolSettings{
		MaxOpenConns:    poolCfg.MaxOpenConns,
		MaxIdleConns:    poolCfg.MaxIdleConns,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewContext(ctx, bunDB)
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	record := testRecord("pg-req", "pg-attempt", lipapi.UsagePlaneProviderBillable, 10, 20)
	if err := store.Record(ctx, record); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	got, err := store.ListByAttempt(ctx, "pg-req", "pg-attempt")
	if err != nil {
		t.Fatalf("ListByAttempt() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListByAttempt len = %d, want 1", len(got))
	}
	assertRoundTrip(t, got[0], record)
}
