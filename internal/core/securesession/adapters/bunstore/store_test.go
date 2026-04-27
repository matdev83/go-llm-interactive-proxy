package bunstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	_ "modernc.org/sqlite"
)

func TestNew_NilBunDB(t *testing.T) {
	t.Parallel()
	_, err := New(nil)
	if err == nil {
		t.Fatal("expected error for nil *bun.DB")
	}
}

func TestNewContext_NilContext(t *testing.T) {
	t.Parallel()
	_, err := NewContext(nil, nil) //nolint:staticcheck // contract: nil ctx must be rejected
	if err == nil {
		t.Fatal("expected error for nil context")
	}
}

func TestNew_AppliesSchema_SQLite(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	var n int
	err := st.db.NewRaw(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='lip_secure_sessions'`,
	).Scan(ctx, &n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("lip_secure_sessions table missing, count=%d", n)
	}
}

func TestSchemaMigrateTwice_Idempotent_SQLite(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	if err := runSecureSessionSchemaMigrate(ctx, st.db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var applied int
	err := st.db.NewRaw(
		`SELECT count(*) FROM bun_securesession_migrations WHERE name = ?`, BaselineMigrationName,
	).Scan(ctx, &applied)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("expected one applied baseline migration row, got %d", applied)
	}
}

func TestCheckReadiness_mandatoryAudit(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	if err := st.CheckReadiness(ctx, domain.PolicyMetadata{AuditMode: "mandatory"}); err != nil {
		t.Fatalf("durable store should not fail mandatory audit: %v", err)
	}
}

func TestUsageTokenTotals_afterAddUsage(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	fp := domain.TokenFingerprint{}
	fp[0] = 9
	fp[31] = 8
	sid := domain.SessionID("sess-usage-rollup-bun")
	_, err := st.Create(ctx, domain.CreateRecord{
		SessionID: sid, ResumeFingerprint: fp,
		Owner: domain.PrincipalRef{ID: "o-rollup"}, Workspace: domain.WorkspaceRef{ID: "w-rollup"},
		ClientHints: domain.ClientHints{ClientSessionID: "c-rollup"},
		Policy:      domain.PolicyMetadata{PolicyVersion: "v1", TranscriptEnabled: false, AuditMode: "optional"},
		ALegID:      "a-rollup", ResumeEligible: true, CreatedAt: time.Unix(5, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	const inWant, outWant int64 = 7, 11
	if err := st.AddUsage(ctx, domain.UsageDelta{
		SessionID: sid, TurnID: "t1", BLegID: "b1",
		InputTokens: inWant, OutputTokens: outWant,
	}); err != nil {
		t.Fatal(err)
	}
	rollup, ok := any(st).(app.SessionUsageRollup)
	if !ok {
		t.Fatal("expected *Store to implement app.SessionUsageRollup")
	}
	in, out, err := rollup.UsageTokenTotals(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if in != inWant || out != outWant {
		t.Fatalf("UsageTokenTotals: got in=%d out=%d want in=%d out=%d", in, out, inWant, outWant)
	}
}

func TestLoadByID_notFound_isDomainError(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	_, err := st.LoadByID(ctx, "nonexistent-session-id-zzzzzzzz")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("got %v want %v", err, domain.ErrSessionNotFound)
	}
}

func TestSummary_errorAfterClose_doesNotEchoDSNMarker(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	_ = st.Close()
	_, err := st.Summary(ctx, domain.SummaryQuery{OwnerID: "o", WorkspaceID: "w", Limit: 3})
	if err == nil {
		t.Fatal("expected error after close")
	}
	msg := strings.ToLower(err.Error())
	// Store never holds a DSN; these markers must not appear in wrapped store errors.
	if strings.Contains(msg, "fake-secret-password") || strings.Contains(msg, "postgres://") {
		t.Fatalf("error should not echo connection material: %v", err)
	}
}

var testMemDBSeq atomic.Int64

func newTestStore(t *testing.T) (*Store, func()) {
	t.Helper()
	return newTestStoreWithOpts(t, Options{})
}

func newTestStoreWithOpts(t *testing.T, opts Options) (*Store, func()) {
	t.Helper()
	id := testMemDBSeq.Add(1)
	dsn := fmt.Sprintf("file:mem%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", id)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	st, err := NewContextWithOptions(context.Background(), bunDB, opts)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	return st, func() { _ = st.Close() }
}

func TestBunStore_sqlMetaCache_appendTranscriptStaleUntilTTL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, cleanup := newTestStoreWithOpts(t, Options{SQLQueryCacheTTL: 50 * time.Millisecond, SQLQueryCacheMaxEntries: 64})
	defer cleanup()
	fp := domain.TokenFingerprint{}
	fp[0] = 1
	cr := domain.CreateRecord{
		SessionID: "bun-append-stale", ResumeFingerprint: fp,
		Owner:     domain.PrincipalRef{ID: "o", Issuer: "i", Tenant: "t"},
		Workspace: domain.WorkspaceRef{ID: "w"}, ClientHints: domain.ClientHints{},
		Policy: domain.PolicyMetadata{
			PolicyVersion: "p", TranscriptEnabled: true, EffectiveTreatment: "strict",
			StricterPolicyResolution: "x", RouteHint: "", RedactionProfile: "r", AuditMode: "a",
		},
		ALegID: "a", CreatedAt: time.Unix(1, 0),
	}
	if _, err := st.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendTranscript(ctx, domain.TranscriptItem{
		SessionID: cr.SessionID, TurnID: "t1", EventKind: "e", PayloadRef: "p", CreatedAt: time.Unix(2, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE lip_secure_sessions SET transcript_enabled = 0 WHERE session_id = ?`, string(cr.SessionID)); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendTranscript(ctx, domain.TranscriptItem{
		SessionID: cr.SessionID, TurnID: "t1", EventKind: "e2", PayloadRef: "p2", CreatedAt: time.Unix(3, 0),
	}); err != nil {
		t.Fatalf("expected cached policy before TTL: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	err := st.AppendTranscript(ctx, domain.TranscriptItem{
		SessionID: cr.SessionID, TurnID: "t1", EventKind: "e3", PayloadRef: "p3", CreatedAt: time.Unix(4, 0),
	})
	if !errors.Is(err, domain.ErrTranscriptDisabled) {
		t.Fatalf("want ErrTranscriptDisabled after TTL got %v", err)
	}
}

func TestBunStore_sqlMetaCache_transcriptObservesStalePolicyUntilTTL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, cleanup := newTestStoreWithOpts(t, Options{SQLQueryCacheTTL: 50 * time.Millisecond, SQLQueryCacheMaxEntries: 64})
	defer cleanup()

	fp := domain.TokenFingerprint{}
	fp[1] = 2
	cr := domain.CreateRecord{
		SessionID: "bun-transcript-stale", ResumeFingerprint: fp,
		Owner:     domain.PrincipalRef{ID: "o", Issuer: "i", Tenant: "t"},
		Workspace: domain.WorkspaceRef{ID: "w"}, ClientHints: domain.ClientHints{},
		Policy: domain.PolicyMetadata{
			PolicyVersion: "p", TranscriptEnabled: true, EffectiveTreatment: "strict",
			StricterPolicyResolution: "x", RouteHint: "", RedactionProfile: "r", AuditMode: "a",
		},
		ALegID: "a", CreatedAt: time.Unix(1, 0),
	}
	if _, err := st.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendTranscript(ctx, domain.TranscriptItem{
		SessionID: cr.SessionID, TurnID: "t1", EventKind: "e", PayloadRef: "p", CreatedAt: time.Unix(2, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Transcript(ctx, cr.SessionID, domain.ReadOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE lip_secure_sessions SET transcript_enabled = 0 WHERE session_id = ?`, string(cr.SessionID)); err != nil {
		t.Fatal(err)
	}
	items, err := st.Transcript(ctx, cr.SessionID, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("want stale read to still return transcript rows got len=%d", len(items))
	}
	time.Sleep(500 * time.Millisecond)
	items2, err := st.Transcript(ctx, cr.SessionID, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items2) != 0 {
		t.Fatalf("want empty after TTL refresh got len=%d", len(items2))
	}
}

func TestBunStore_transcriptEnabledCached_missingRowMapsToSessionNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, cleanup := newTestStoreWithOpts(t, Options{SQLQueryCacheTTL: time.Hour, SQLQueryCacheMaxEntries: 64})
	defer cleanup()

	sid := domain.SessionID("no-such-session-for-policy-read")
	for range 2 {
		_, err := st.transcriptEnabledCached(ctx, st.db, sid)
		if !errors.Is(err, domain.ErrSessionNotFound) {
			t.Fatalf("got %v want ErrSessionNotFound", err)
		}
	}
}

func TestSummary_returnsDomainFieldsOnly(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	fp := domain.TokenFingerprint{}
	fp[0] = 1
	fp[31] = 2
	_, err := st.Create(ctx, domain.CreateRecord{
		SessionID: "sess-sum-fields", ResumeFingerprint: fp,
		Owner: domain.PrincipalRef{ID: "owner1"}, Workspace: domain.WorkspaceRef{ID: "ws1"},
		ClientHints: domain.ClientHints{ClientSessionID: "c1"},
		Policy:      domain.PolicyMetadata{PolicyVersion: "pv", TranscriptEnabled: false, AuditMode: "optional"},
		ALegID:      "a1", ResumeEligible: true, CreatedAt: time.Unix(10, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	sums, err := st.Summary(ctx, domain.SummaryQuery{OwnerID: "owner1", WorkspaceID: "ws1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 1 {
		t.Fatalf("want 1 summary, got %d", len(sums))
	}
	s := sums[0]
	if s.SessionID != "sess-sum-fields" || s.OwnerID != "owner1" || s.WorkspaceID != "ws1" {
		t.Fatalf("unexpected projection: %+v", s)
	}
}
