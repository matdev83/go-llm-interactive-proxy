package ledgerstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/ledger"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestSQLiteLedgerStore_recordsAndListsByRequestAndAttempt(t *testing.T) {
	t.Parallel()

	store := newSQLiteStoreForTest(t)
	ctx := context.Background()
	records := []ledger.Record{
		testRecord("req-1", "attempt-1", lipapi.UsagePlaneProviderBillable, 10, 2),
		testRecord("req-1", "attempt-2", lipapi.UsagePlaneProviderBillable, 11, 3),
		testRecord("req-1", "attempt-2", lipapi.UsagePlaneClientVisible, 12, 4),
	}
	for _, record := range records {
		if err := store.Record(ctx, record); err != nil {
			t.Fatalf("Record() error = %v", err)
		}
	}

	got, err := store.ListByRequest(ctx, "req-1")
	if err != nil {
		t.Fatalf("ListByRequest() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListByRequest() len = %d, want 3", len(got))
	}
	assertRoundTrip(t, got[0], records[0])
	assertRoundTrip(t, got[1], records[1])
	assertRoundTrip(t, got[2], records[2])

	attempt, err := store.ListByAttempt(ctx, "req-1", "attempt-2")
	if err != nil {
		t.Fatalf("ListByAttempt() error = %v", err)
	}
	if len(attempt) != 2 {
		t.Fatalf("ListByAttempt() len = %d, want 2", len(attempt))
	}
	if attempt[0].Plane == attempt[1].Plane {
		t.Fatalf("attempt records were merged: %#v", attempt)
	}
}

func TestSQLiteLedgerStore_appendOnlyAndContextCancellation(t *testing.T) {
	t.Parallel()

	store := newSQLiteStoreForTest(t)
	ctx := context.Background()
	record := testRecord("req-dup", "attempt-1", lipapi.UsagePlaneProviderBillable, 1, 2)
	if err := store.Record(ctx, record); err != nil {
		t.Fatalf("first Record() error = %v", err)
	}
	if err := store.Record(ctx, record); err != nil {
		t.Fatalf("second Record() error = %v", err)
	}
	got, err := store.ListByRequest(ctx, "req-dup")
	if err != nil {
		t.Fatalf("ListByRequest() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("append-only duplicate count = %d, want 2", len(got))
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Record(canceled, record); !errors.Is(err, context.Canceled) {
		t.Fatalf("Record(canceled) error = %v, want context.Canceled", err)
	}
	if _, err := store.ListByRequest(canceled, "req-dup"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListByRequest(canceled) error = %v, want context.Canceled", err)
	}
}

func TestSQLiteLedgerStore_migrationCreatesIndexes(t *testing.T) {
	t.Parallel()

	store := newSQLiteStoreForTest(t)
	ctx := context.Background()
	var tableName string
	if err := store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'token_accounting_ledger_records'`).Scan(ctx, &tableName); err != nil {
		t.Fatalf("table lookup: %v", err)
	}
	if tableName != "token_accounting_ledger_records" {
		t.Fatalf("table name = %q", tableName)
	}
	for _, index := range []string{"idx_token_accounting_ledger_request", "idx_token_accounting_ledger_request_attempt"} {
		var name string
		if err := store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(ctx, &name); err != nil {
			t.Fatalf("index %s lookup: %v", index, err)
		}
		if name != index {
			t.Fatalf("index name = %q, want %q", name, index)
		}
	}
}

func TestRecordStructHasNoRawContentColumns(t *testing.T) {
	t.Parallel()

	record := testRecord("req-safe", "attempt-1", lipapi.UsagePlaneProviderBillable, 1, 1)
	if record.Metadata.Tokenizer.ID == "" {
		t.Fatal("test setup expected tokenizer metadata")
	}
	for _, field := range []string{record.RequestID, record.AttemptID, record.Backend, record.Model, record.UnavailableReason, record.FailureReason} {
		if field == "secret prompt" || field == "raw completion" {
			t.Fatalf("raw content field stored: %q", field)
		}
	}
}

func newSQLiteStoreForTest(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := NewContext(context.Background(), bunDB)
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testRecord(requestID, attemptID string, plane lipapi.UsagePlane, input, output int) ledger.Record {
	createdAt := time.Date(2026, 5, 14, 10, 30, 0, 123456789, time.UTC)
	return ledger.Record{
		RequestID:         requestID,
		AttemptID:         attemptID,
		Backend:           "openai",
		Model:             "gpt-4.1-mini",
		Plane:             plane,
		InputTokens:       input,
		OutputTokens:      output,
		CacheReadTokens:   3,
		CacheWriteTokens:  4,
		ReasoningTokens:   5,
		TotalTokens:       input + output + 12,
		Metadata:          lipapi.UsageAccountingMetadata{Plane: plane, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative, Tokenizer: lipapi.TokenizerRef{Type: "provider", ID: "openai", Version: "2026-05-14", Source: "provider", ModelUsed: "gpt-4.1-mini"}},
		CreatedAt:         createdAt,
		UnavailableReason: "",
		FailureReason:     "rate_limited_pre_output",
	}
}

func assertRoundTrip(t *testing.T, got, want ledger.Record) {
	t.Helper()
	if got.RequestID != want.RequestID || got.AttemptID != want.AttemptID || got.Backend != want.Backend || got.Model != want.Model || got.Plane != want.Plane {
		t.Fatalf("identity = %#v, want %#v", got, want)
	}
	if got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens || got.CacheReadTokens != want.CacheReadTokens || got.CacheWriteTokens != want.CacheWriteTokens || got.ReasoningTokens != want.ReasoningTokens || got.TotalTokens != want.TotalTokens {
		t.Fatalf("tokens = %#v, want %#v", got, want)
	}
	if got.Metadata != want.Metadata {
		t.Fatalf("metadata = %#v, want %#v", got.Metadata, want.Metadata)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("created_at = %s, want %s", got.CreatedAt, want.CreatedAt)
	}
	if got.FailureReason != want.FailureReason || got.UnavailableReason != want.UnavailableReason {
		t.Fatalf("reasons = (%q,%q), want (%q,%q)", got.UnavailableReason, got.FailureReason, want.UnavailableReason, want.FailureReason)
	}
}
