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

// TestPostgresProviderCostWorkBackfillAcceptsTextSealedAtLegs proves the
// 20260822000000 upgrade INSERT no longer raises SQLSTATE 42804 when copying
// from usage_leg_records.sealed_at (TEXT) into TIMESTAMPTZ work columns.
func TestPostgresProviderCostWorkBackfillAcceptsTextSealedAtLegs(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bunDB, err := db.OpenPostgresBun(ctx, dsn, db.PoolSettings{MaxOpenConns: 2, MaxIdleConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bunDB.Close() })

	schema := fmt.Sprintf("r2_pcw_%d", time.Now().UnixNano())
	if _, err := bunDB.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = bunDB.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})
	if _, err := bunDB.ExecContext(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatal(err)
	}
	setup := []string{
		`CREATE TABLE usage_leg_records (
			usage_leg_key TEXT PRIMARY KEY,
			call_id TEXT NOT NULL,
			sealed_at TEXT NOT NULL
		)`,
		`INSERT INTO usage_leg_records(usage_leg_key, call_id, sealed_at)
		 VALUES ('leg-1', 'call-1', '2026-08-15T12:00:00Z')`,
	}
	for _, statement := range setup {
		if _, err := bunDB.ExecContext(ctx, statement); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	for _, statement := range postgresProviderCostWorkDDL() {
		if _, err := bunDB.ExecContext(ctx, statement); err != nil {
			t.Fatalf("provider_cost_work DDL: %v\nSQL: %s", err, statement)
		}
	}
	var count int
	if err := bunDB.NewRaw(`SELECT COUNT(1) FROM provider_cost_work WHERE usage_leg_key = ?`, "leg-1").Scan(ctx, &count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("backfilled rows = %d, want 1", count)
	}
}
