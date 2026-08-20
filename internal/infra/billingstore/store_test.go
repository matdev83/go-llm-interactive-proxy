package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	_ "modernc.org/sqlite"
)

func TestSQLiteBillingSchemaCreatesRequiredTablesAndIndexes(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	for _, table := range []string{
		"billing_accounts", "billing_account_policy_events",
		"usage_leg_records", "usage_call_records", "provider_cost_work", "call_exposures",
		"journal_transactions", "journal_entries", "bun_billing_migrations",
	} {
		var got string
		if err := store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(ctx, &got); err != nil {
			t.Fatalf("table %s lookup: %v", table, err)
		}
		if got != table {
			t.Fatalf("table = %q, want %q", got, table)
		}
	}
	var holdTable string
	if err := store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, "authorization_holds").Scan(ctx, &holdTable); err == nil {
		t.Fatalf("authorization_holds should be dropped after migrations, found %q", holdTable)
	} else if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("authorization_holds absence lookup: %v", err)
	}
	for _, index := range []string{
		"idx_billing_journal_account_sequence",
		"idx_billing_journal_source",
		journalReversalUniqueIndex,
		usageLegCallBLegIndex,
		usageCallCallIDIndex,
		usageCallAccountSessionIndex,
		usageCallClaimStatusIndex,
		usageCallClaimPendingIndex,
		providerCostWorkStatusIndex,
		providerCostWorkPendingIndex,
		exposureAccountStatusIndex,
	} {
		var got string
		if err := store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(ctx, &got); err != nil {
			t.Fatalf("index %s lookup: %v", index, err)
		}
		if got != index {
			t.Fatalf("index = %q, want %q", got, index)
		}
	}
	if err := VerifySchema(ctx, store.db); err != nil {
		t.Fatalf("VerifySchema: %v", err)
	}
}

func TestSQLiteBillingSchemaMigrationIsIdempotent(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	if err := Migrate(context.Background(), store.db); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

func newSQLiteTestStore(t *testing.T) *DurableStore {
	t.Helper()
	dsn := fmt.Sprintf("file:billing-schema-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", testSequence.Add(1))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(16)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: "test"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

var testSequence atomic.Int64
