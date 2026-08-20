package billingstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun/dialect"
)

func TestFreshSchemaDoesNotCreateRetiredUsageAppendOutbox(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'usage_append_outbox'`).Scan(ctx, &count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("fresh schema created retired usage_append_outbox: count=%d", count)
	}
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM bun_billing_migrations WHERE name = ?`, UsageAppendOutboxMigrationName).Scan(ctx, &count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("fresh schema recorded retired outbox creation migration: count=%d", count)
	}
	if err := VerifySchema(ctx, store.db); err != nil {
		t.Fatal(err)
	}
}

func TestCutoverAPIIsCentralStoreBound(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	_ = (func(context.Context) error)(store.CutoverUsageAppendOutbox)
}

func TestCutoverProvesDeliveryInCentralStore(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ensureLegacyUsageAppendOutbox(t, store)
	ctx := context.Background()
	call := testOutboxCall(t)
	if err := store.EnqueueCallUsageAppend(ctx, call, "legacy"); err != nil {
		t.Fatal(err)
	}
	if err := store.CutoverUsageAppendOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetCallUsage(ctx, call.CallID); err != nil {
		t.Fatalf("central replay missing after cutover: %v", err)
	}
}

func TestRetirementMigrationDrainsUpgradedOutboxBeforeDrop(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ensureLegacyUsageAppendOutbox(t, store)
	ctx := context.Background()
	call := testOutboxCall(t)
	if err := store.EnqueueCallUsageAppend(ctx, call, "upgrade"); err != nil {
		t.Fatal(err)
	}
	if err := usageAppendOutboxRetirementSchemaUp(ctx, store.db); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetCallUsage(ctx, call.CallID); err != nil {
		t.Fatalf("upgraded outbox row was not replayed centrally: %v", err)
	}
	if exists, err := usageAppendOutboxTableExists(ctx, store.db); err != nil || exists {
		t.Fatalf("retired outbox table exists=%v err=%v", exists, err)
	}
}

func TestMigrateRetiresHistoricalUsageAppendOutbox(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	// Reconstruct the brownfield state: the historical migration is recorded
	// but no longer registered, its table still exists, and one row is pending.
	if _, err := store.db.NewRaw(`DELETE FROM bun_billing_migrations WHERE name = ?`, UsageAppendOutboxRetirementMigrationName).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	for _, statement := range sqliteUsageAppendOutboxDDL() {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("create historical outbox fixture: %v", err)
		}
	}
	if _, err := store.db.NewRaw(`INSERT INTO bun_billing_migrations(name, group_id, migrated_at) VALUES (?, ?, ?)`, UsageAppendOutboxMigrationName, 99, time.Now().UTC()).Exec(ctx); err != nil {
		t.Fatalf("record unknown historical migration: %v", err)
	}
	call := testOutboxCall(t)
	sealed, err := call.Seal()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`INSERT INTO usage_append_outbox(append_key,kind,call_id,payload_json,status,attempt_count,next_attempt_at,last_error,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, sealed.Key, "call", sealed.CallID.String(), string(payload), "pending", 0, time.Now().UTC(), "", time.Now().UTC(), time.Now().UTC()).Exec(ctx); err != nil {
		t.Fatalf("seed historical pending row: %v", err)
	}

	// This must exercise the public migration runner, not the migration up
	// function directly. Bun must tolerate the unknown historical name and run
	// only the newly registered forward retirement migration.
	if err := Migrate(ctx, store.db); err != nil {
		t.Fatalf("Migrate brownfield retirement: %v", err)
	}
	if _, err := store.GetCallUsage(ctx, sealed.CallID); err != nil {
		t.Fatalf("pending row was not centrally replayed: %v", err)
	}
	if exists, err := usageAppendOutboxTableExists(ctx, store.db); err != nil || exists {
		t.Fatalf("historical outbox exists=%v err=%v", exists, err)
	}
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM bun_billing_migrations WHERE name = ?`, UsageAppendOutboxMigrationName).Scan(ctx, &count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("historical migration marker count=%d, want 1", count)
	}
	if err := VerifySchema(ctx, store.db); err != nil {
		t.Fatalf("VerifySchema after brownfield retirement: %v", err)
	}
}

func TestMigrateDoesNotRecordBlockedRetirement(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if _, err := store.db.NewRaw(`DELETE FROM bun_billing_migrations WHERE name = ?`, UsageAppendOutboxRetirementMigrationName).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE usage_append_outbox (append_key TEXT PRIMARY KEY, kind TEXT NOT NULL, call_id TEXT NOT NULL, payload_json TEXT NOT NULL, status TEXT NOT NULL, attempt_count INTEGER NOT NULL, next_attempt_at TEXT NOT NULL, last_error TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`INSERT INTO usage_append_outbox(append_key,kind,call_id,payload_json,status,attempt_count,next_attempt_at,last_error,created_at,updated_at) VALUES ('blocked','call','bc_00000000000000000000000000000099','{','pending',0,?,?,?,?)`, time.Now().UTC(), "", time.Now().UTC(), time.Now().UTC()).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, store.db); err == nil {
		t.Fatal("blocked retirement must fail")
	}
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM bun_billing_migrations WHERE name = ?`, UsageAppendOutboxRetirementMigrationName).Scan(ctx, &count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("blocked retirement was recorded as applied: count=%d", count)
	}
}

func TestCutoverUsesDialectPlaceholders(t *testing.T) {
	t.Parallel()
	if got := cutoverBeginStatement(dialect.SQLite); got != "BEGIN IMMEDIATE" {
		t.Fatalf("SQLite cutover begin = %q, want BEGIN IMMEDIATE", got)
	}
	if got := cutoverLockStatement(dialect.SQLite); got != "" {
		t.Fatalf("SQLite cutover lock = %q, want empty", got)
	}
	if got := cutoverBeginStatement(dialect.PG); got != "BEGIN" {
		t.Fatalf("PostgreSQL cutover begin = %q, want BEGIN", got)
	}
	if got := cutoverLockStatement(dialect.PG); got != "LOCK TABLE usage_append_outbox IN ACCESS EXCLUSIVE MODE" {
		t.Fatalf("PostgreSQL cutover lock = %q", got)
	}
	if got := cutoverPlaceholder(dialect.PG, 1); got != "$1" {
		t.Fatalf("PostgreSQL placeholder = %q, want $1", got)
	}
	if strings.Contains("SELECT fingerprint FROM usage_call_records WHERE usage_call_key = "+cutoverPlaceholder(dialect.PG, 1), "?") {
		t.Fatal("PostgreSQL reconciliation SQL still contains SQLite placeholder")
	}
	if got := cutoverPlaceholder(dialect.SQLite, 1); got != "?" {
		t.Fatalf("SQLite placeholder = %q, want ?", got)
	}
}
