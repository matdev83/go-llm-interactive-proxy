package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
)

func TestPhase6LegacyRetirementFreshSchemaConverges(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	assertLegacyTablesAbsent(t, store.db)
	if err := VerifySchema(context.Background(), store.db); err != nil {
		t.Fatalf("VerifySchema after retirement: %v", err)
	}
}

func TestPhase6LegacyRetirementAllowsOnlyProvenProcessedRows(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	createLegacyRetirementTables(t, store.db)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO turn_usage_records(tur_key,fingerprint,payload_json) VALUES ('tur-processed','fp','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO leg_usage_records(lur_key,tur_key,fingerprint,payload_json) VALUES ('lur-processed','tur-processed','lfp','{}')`); err != nil {
		t.Fatal(err)
	}
	createDurableSettlementProof(t, store, "tur-processed")
	if _, err := store.db.ExecContext(ctx, `INSERT INTO usage_record_processing(tur_key,tur_fingerprint,status,result_ref) VALUES ('tur-processed','fp','processed','customer-settlement:v1:tur-processed')`); err != nil {
		t.Fatal(err)
	}
	if err := retireLegacyUsagePersistence(ctx, store.db); err != nil {
		t.Fatalf("processed historical rows should be provably discardable: %v", err)
	}
	assertLegacyTablesAbsent(t, store.db)
}

func TestPhase6LegacyRetirementBlocksEveryUnresolvedProcessingState(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"pending", "processing", "retryable", "error", "unreconciled_cost", "terminal_error"} {
		t.Run(status, func(t *testing.T) {
			store := newSQLiteTestStore(t)
			createLegacyRetirementTables(t, store.db)
			ctx := context.Background()
			if _, err := store.db.ExecContext(ctx, `INSERT INTO turn_usage_records(tur_key,fingerprint,payload_json) VALUES ('tur-block','fp','{}')`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `INSERT INTO usage_record_processing(tur_key,tur_fingerprint,status,result_ref) VALUES ('tur-block','fp',?, '')`, status); err != nil {
				t.Fatal(err)
			}
			if err := retireLegacyUsagePersistence(ctx, store.db); err == nil || !errors.Is(err, ErrLegacyUsageRetirementBlocked) {
				t.Fatalf("retirement error = %v, want actionable blocked error", err)
			}
			assertLegacyTablesPresent(t, store.db)
		})
	}
}

func TestPhase6LegacyRetirementBlocksMalformedOrUnprovableRows(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		insert string
	}{
		{name: "malformed TUR", insert: `INSERT INTO turn_usage_records(tur_key,fingerprint,payload_json) VALUES ('tur-bad','fp','{')`},
		{name: "missing processing proof", insert: `INSERT INTO turn_usage_records(tur_key,fingerprint,payload_json) VALUES ('tur-no-proof','fp','{}')`},
		{name: "processed without result", insert: `INSERT INTO usage_record_processing(tur_key,tur_fingerprint,status,result_ref) VALUES ('tur-no-proof','fp','processed','')`},
		{name: "fingerprint conflict", insert: `INSERT INTO usage_record_processing(tur_key,tur_fingerprint,status,result_ref) VALUES ('tur-conflict','different','processed','customer-settlement:v1:tur-conflict')`},
		{name: "arbitrary result reference", insert: `INSERT INTO usage_record_processing(tur_key,tur_fingerprint,status,result_ref) VALUES ('tur-arbitrary','fp','processed','arbitrary-string')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newSQLiteTestStore(t)
			createLegacyRetirementTables(t, store.db)
			ctx := context.Background()
			if _, err := store.db.ExecContext(ctx, tc.insert); err != nil {
				t.Fatal(err)
			}
			if tc.name == "fingerprint conflict" {
				if _, err := store.db.ExecContext(ctx, `INSERT INTO turn_usage_records(tur_key,fingerprint,payload_json) VALUES ('tur-conflict','fp','{}')`); err != nil {
					t.Fatal(err)
				}
			}
			if err := retireLegacyUsagePersistence(ctx, store.db); err == nil || !errors.Is(err, ErrLegacyUsageRetirementBlocked) {
				t.Fatalf("retirement error = %v, want blocked", err)
			}
		})
	}
}

func TestPhase6LegacyRetirementSQLiteWriterRaceIsClosedByCriticalSection(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	createLegacyRetirementTables(t, store.db)
	ctx := context.Background()
	started := make(chan struct{})
	var once sync.Once
	proofHook := func() { once.Do(func() { close(started) }) }

	writerDone := make(chan error, 1)
	go func() {
		<-started
		_, err := store.db.ExecContext(ctx, `INSERT INTO turn_usage_records(tur_key,fingerprint,payload_json) VALUES ('race','fp','{}')`)
		writerDone <- err
	}()
	if err := retireLegacyUsagePersistenceWithHook(ctx, store.db, proofHook); err != nil {
		t.Fatalf("empty retirement: %v", err)
	}
	if err := <-writerDone; err == nil {
		t.Fatal("legacy writer committed after retirement dropped its table")
	}
	assertLegacyTablesAbsent(t, store.db)
}

func createDurableSettlementProof(t *testing.T, store *DurableStore, turKey string) {
	t.Helper()
	ctx := context.Background()
	account := billing.Account{ID: "legacy-proof-account", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	ref := "customer-settlement:v1:" + turKey
	if _, err := store.db.ExecContext(ctx, `INSERT INTO billing_operation_snapshots(operation_key,account_id,operation_kind,source_key,fingerprint,integrity_fingerprint,currency,mode,balance_before_nano,balance_after_nano,reserved_before_nano,reserved_after_nano,spendable_before_nano,spendable_after_nano,credit_floor_nano,credit_limit_nano,version_before,version_after,account_sequence_start,account_sequence_end,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, ref, account.ID, "customer_settlement", turKey, "result-fingerprint", "integrity-fingerprint", "USD", "prepaid", 100, 100, 0, 0, 100, 100, 0, 0, 1, 1, 0, 0, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func createLegacyRetirementTables(t *testing.T, db *bun.DB) {
	t.Helper()
	ctx := context.Background()
	for _, statement := range []string{
		`CREATE TABLE turn_usage_records (tur_key TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, payload_json TEXT NOT NULL)`,
		`CREATE TABLE leg_usage_records (lur_key TEXT PRIMARY KEY, tur_key TEXT NOT NULL, fingerprint TEXT NOT NULL, payload_json TEXT NOT NULL)`,
		`CREATE TABLE usage_record_processing (tur_key TEXT PRIMARY KEY, tur_fingerprint TEXT NOT NULL, status TEXT NOT NULL, result_ref TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX idx_billing_processing_status ON usage_record_processing(status)`,
		`CREATE TRIGGER billing_tur_immutable_delete BEFORE DELETE ON turn_usage_records BEGIN SELECT RAISE(ABORT, 'immutable'); END`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
}

func assertLegacyTablesAbsent(t *testing.T, db *bun.DB) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"turn_usage_records", "leg_usage_records", "usage_record_processing"} {
		var name string
		err := db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(ctx, &name)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("legacy table %s remains (name=%q err=%v)", table, name, err)
		}
	}
	for _, object := range []string{"idx_billing_processing_status", sessionAccountIndex, "billing_tur_immutable_update", "billing_tur_immutable_delete", "billing_lur_immutable_update", "billing_lur_immutable_delete"} {
		var name string
		if err := db.NewRaw(`SELECT name FROM sqlite_master WHERE name = ?`, object).Scan(ctx, &name); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("retired legacy object %s remains (name=%q err=%v)", object, name, err)
		}
	}
}

func assertLegacyTablesPresent(t *testing.T, db *bun.DB) {
	t.Helper()
	ctx := context.Background()
	var count int
	if err := db.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name IN ('turn_usage_records','leg_usage_records','usage_record_processing')`).Scan(ctx, &count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("legacy table count = %d, want 3", count)
	}
}
