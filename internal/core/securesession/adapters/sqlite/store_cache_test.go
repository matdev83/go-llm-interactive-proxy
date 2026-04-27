package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
)

//nolint:paralleltest // serial: TTL edge cases compete poorly with t.Parallel under full-suite CPU load
func TestStore_sqlMetaCache_appendTranscriptStaleUntilTTL(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "c.db")
	const metaTTL = 500 * time.Millisecond
	s, err := OpenContextWithOptions(ctx, path, Options{SQLQueryCacheTTL: metaTTL, SQLQueryCacheMaxEntries: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	fp := domain.TokenFingerprint{}
	fp[0] = 1
	cr := domain.CreateRecord{
		SessionID: "s-append-stale", ResumeFingerprint: fp,
		Owner:     domain.PrincipalRef{ID: "o", Issuer: "i", Tenant: "t"},
		Workspace: domain.WorkspaceRef{ID: "w"}, ClientHints: domain.ClientHints{},
		Policy: domain.PolicyMetadata{
			PolicyVersion: "p", TranscriptEnabled: true, EffectiveTreatment: "strict",
			StricterPolicyResolution: "x", RouteHint: "", RedactionProfile: "r", AuditMode: "a",
		},
		ALegID: "a", CreatedAt: time.Unix(1, 0),
	}
	if _, err := s.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	item := domain.TranscriptItem{
		SessionID: cr.SessionID, TurnID: "t1", EventKind: "e", PayloadRef: "p", CreatedAt: time.Unix(2, 0),
	}
	if err := s.AppendTranscript(ctx, item); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE lip_secure_sessions SET transcript_enabled = 0 WHERE session_id = ?`, string(cr.SessionID)); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTranscript(ctx, domain.TranscriptItem{
		SessionID: cr.SessionID, TurnID: "t1", EventKind: "e2", PayloadRef: "p2", CreatedAt: time.Unix(3, 0),
	}); err != nil {
		t.Fatalf("expected cached policy to allow append before TTL: %v", err)
	}
	time.Sleep(metaTTL + 150*time.Millisecond)
	if err := s.AppendTranscript(ctx, domain.TranscriptItem{
		SessionID: cr.SessionID, TurnID: "t1", EventKind: "e3", PayloadRef: "p3", CreatedAt: time.Unix(4, 0),
	}); err != domain.ErrTranscriptDisabled {
		t.Fatalf("want ErrTranscriptDisabled after TTL got %v", err)
	}
}

//nolint:paralleltest // serial: see TestStore_sqlMetaCache_appendTranscriptStaleUntilTTL
func TestStore_sqlMetaCache_transcriptObservesStalePolicyUntilTTL(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	const metaTTL = 500 * time.Millisecond
	s, err := OpenContextWithOptions(ctx, filepath.Join(dir, "t.db"), Options{SQLQueryCacheTTL: metaTTL, SQLQueryCacheMaxEntries: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	fp := domain.TokenFingerprint{}
	fp[1] = 2
	cr := domain.CreateRecord{
		SessionID: "s-tr-stale", ResumeFingerprint: fp,
		Owner:     domain.PrincipalRef{ID: "o", Issuer: "i", Tenant: "t"},
		Workspace: domain.WorkspaceRef{ID: "w"}, ClientHints: domain.ClientHints{},
		Policy: domain.PolicyMetadata{
			PolicyVersion: "p", TranscriptEnabled: true, EffectiveTreatment: "strict",
			StricterPolicyResolution: "x", RouteHint: "", RedactionProfile: "r", AuditMode: "a",
		},
		ALegID: "a", CreatedAt: time.Unix(1, 0),
	}
	if _, err := s.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTranscript(ctx, domain.TranscriptItem{
		SessionID: cr.SessionID, TurnID: "t1", EventKind: "e", PayloadRef: "p", CreatedAt: time.Unix(2, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transcript(ctx, cr.SessionID, domain.ReadOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE lip_secure_sessions SET transcript_enabled = 0 WHERE session_id = ?`, string(cr.SessionID)); err != nil {
		t.Fatal(err)
	}
	items, err := s.Transcript(ctx, cr.SessionID, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("want stale read to still return transcript rows got len=%d", len(items))
	}
	time.Sleep(metaTTL + 150*time.Millisecond)
	items2, err := s.Transcript(ctx, cr.SessionID, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items2) != 0 {
		t.Fatalf("want empty after TTL refresh got len=%d", len(items2))
	}
}

func TestStore_sqlMetaCache_negativeExistsCreateStillWorks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	s, err := OpenContextWithOptions(ctx, filepath.Join(dir, "n.db"), Options{SQLQueryCacheTTL: time.Hour, SQLQueryCacheMaxEntries: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	sid := domain.SessionID("new-sess")
	if _, err := s.Transcript(ctx, sid, domain.ReadOptions{}); err != domain.ErrSessionNotFound {
		t.Fatalf("got %v", err)
	}
	fp := domain.TokenFingerprint{}
	fp[2] = 3
	cr := domain.CreateRecord{
		SessionID: sid, ResumeFingerprint: fp,
		Owner:     domain.PrincipalRef{ID: "o", Issuer: "i", Tenant: "t"},
		Workspace: domain.WorkspaceRef{ID: "w"}, ClientHints: domain.ClientHints{},
		Policy: domain.PolicyMetadata{
			PolicyVersion: "p", TranscriptEnabled: true, EffectiveTreatment: "strict",
			StricterPolicyResolution: "x", RouteHint: "", RedactionProfile: "r", AuditMode: "a",
		},
		ALegID: "a", CreatedAt: time.Unix(1, 0),
	}
	if _, err := s.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transcript(ctx, sid, domain.ReadOptions{}); err != nil {
		t.Fatalf("after create: %v", err)
	}
}

func TestStore_noCache_appendTranscriptDisabledImmediate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	s, err := OpenContext(ctx, filepath.Join(dir, "nc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	fp := domain.TokenFingerprint{}
	fp[3] = 4
	cr := domain.CreateRecord{
		SessionID: "s-nc", ResumeFingerprint: fp,
		Owner:     domain.PrincipalRef{ID: "o", Issuer: "i", Tenant: "t"},
		Workspace: domain.WorkspaceRef{ID: "w"}, ClientHints: domain.ClientHints{},
		Policy: domain.PolicyMetadata{
			PolicyVersion: "p", TranscriptEnabled: true, EffectiveTreatment: "strict",
			StricterPolicyResolution: "x", RouteHint: "", RedactionProfile: "r", AuditMode: "a",
		},
		ALegID: "a", CreatedAt: time.Unix(1, 0),
	}
	if _, err := s.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE lip_secure_sessions SET transcript_enabled = 0 WHERE session_id = ?`, string(cr.SessionID)); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTranscript(ctx, domain.TranscriptItem{
		SessionID: cr.SessionID, TurnID: "t1", EventKind: "e", PayloadRef: "p", CreatedAt: time.Unix(2, 0),
	}); err != domain.ErrTranscriptDisabled {
		t.Fatalf("want immediate disabled without cache got %v", err)
	}
}

func TestStore_transcriptEnabledCached_missingRowMapsToSessionNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := OpenContextWithOptions(ctx, filepath.Join(t.TempDir(), "nr.db"), Options{SQLQueryCacheTTL: time.Hour, SQLQueryCacheMaxEntries: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	sid := domain.SessionID("no-such-session-for-policy-read")
	for range 2 {
		_, err := s.transcriptEnabledCached(ctx, s.db, sid)
		if !errors.Is(err, domain.ErrSessionNotFound) {
			t.Fatalf("got %v want ErrSessionNotFound", err)
		}
	}
}
