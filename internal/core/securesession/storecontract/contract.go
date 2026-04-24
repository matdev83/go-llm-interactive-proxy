// Package storecontract holds reusable contract tests for [app.Store] implementations.
package storecontract

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
)

// RunAll exercises the secure-session store port (memory, SQLite, etc.).
func RunAll(t *testing.T, newStore func() app.Store) {
	t.Helper()
	ctx := context.Background()

	t.Run("Create_uniqueness_sessionID", func(t *testing.T) {
		s := newStore()
		fp1, fp2 := twoFingerprints()
		base := sampleCreate("owner-a", "ws-1", fp1, "a-leg-1", "sid-1")
		_, err := s.Create(ctx, base)
		if err != nil {
			t.Fatal(err)
		}
		dup := base
		dup.ResumeFingerprint = fp2
		dup.ALegID = "a-leg-2"
		_, err = s.Create(ctx, dup)
		if !errors.Is(err, domain.ErrDuplicateSessionID) {
			t.Fatalf("Create duplicate session id: got %v want %v", err, domain.ErrDuplicateSessionID)
		}
	})

	t.Run("Create_uniqueness_fingerprint", func(t *testing.T) {
		s := newStore()
		fp, _ := twoFingerprints()
		r1 := sampleCreate("o1", "w1", fp, "aleg-1", "session-1")
		_, err := s.Create(ctx, r1)
		if err != nil {
			t.Fatal(err)
		}
		r2 := sampleCreate("o1", "w1", fp, "aleg-2", "session-2")
		_, err = s.Create(ctx, r2)
		if !errors.Is(err, domain.ErrDuplicateFingerprint) {
			t.Fatalf("Create duplicate fingerprint: got %v want %v", err, domain.ErrDuplicateFingerprint)
		}
	})

	t.Run("LoadByID_owner_workspace", func(t *testing.T) {
		s := newStore()
		fp, _ := twoFingerprints()
		cr := sampleCreate("principal-x", "workspace-y", fp, "a-leg-z", "sess-own")
		created, err := s.Create(ctx, cr)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.LoadByID(ctx, created.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Owner.ID != "principal-x" || got.Workspace.ID != "workspace-y" {
			t.Fatalf("owner/workspace: got %+v", got)
		}
	})

	t.Run("LoadByALegID_roundTrip", func(t *testing.T) {
		s := newStore()
		fp, _ := twoFingerprints()
		cr := sampleCreate("o", "w", fp, "a-leg-lookup", "sess-aleg")
		created, err := s.Create(ctx, cr)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.LoadByALegID(ctx, "a-leg-lookup")
		if err != nil {
			t.Fatal(err)
		}
		if got.SessionID != created.SessionID {
			t.Fatalf("session id mismatch")
		}
	})

	t.Run("AttemptTrace_outcome_by_BLeg", func(t *testing.T) {
		s := newStore()
		fp, _ := twoFingerprints()
		cr := sampleCreate("o", "w", fp, "a-main", "sess-trace")
		rec, err := s.Create(ctx, cr)
		if err != nil {
			t.Fatal(err)
		}
		tr := domain.AttemptTrace{
			SessionID: rec.SessionID, TurnID: "t1", ALegID: "a-main", BLegID: "b-1",
			AttemptSeq: 1, RequestedModel: "m", ResolvedBackend: "be", ResolvedModel: "rm",
			RouteSource: "rs", RouteReason: "rr", StartedAt: time.Unix(10, 0),
		}
		if err := s.AppendAttemptTrace(ctx, tr); err != nil {
			t.Fatal(err)
		}
		out := domain.AttemptOutcome{
			SessionID: rec.SessionID, TurnID: "t1", BLegID: "b-1", Success: true,
			SurfaceState: domain.SurfaceSurfaced, EndedAt: time.Unix(11, 0),
		}
		if err := s.UpdateAttemptOutcome(ctx, out); err != nil {
			t.Fatal(err)
		}
		loaded, err := s.LoadByID(ctx, rec.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.LatestAttemptTrace.BLegID != "b-1" || loaded.LatestAttemptOutcome.BLegID != "b-1" || !loaded.LatestAttemptOutcome.Success {
			t.Fatalf("trace/outcome: trace=%+v outcome=%+v", loaded.LatestAttemptTrace, loaded.LatestAttemptOutcome)
		}
	})

	t.Run("Transcript_disabled_explicit", func(t *testing.T) {
		s := newStore()
		fp, _ := twoFingerprints()
		cr := sampleCreate("o", "w", fp, "a-t", "sess-tx-off")
		cr.Policy.TranscriptEnabled = false
		rec, err := s.Create(ctx, cr)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Policy.TranscriptEnabled {
			t.Fatal("expected transcript disabled on record")
		}
		items, err := s.Transcript(ctx, rec.SessionID, domain.ReadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		assertNonNilEmptySlice(t, items)
	})

	t.Run("AddUsage_summary_rollups", func(t *testing.T) {
		s := newStore()
		fp, _ := twoFingerprints()
		cr := sampleCreate("owner-sum", "ws-sum", fp, "a-sum", "sess-sum")
		rec, err := s.Create(ctx, cr)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AppendAttemptTrace(ctx, domain.AttemptTrace{
			SessionID: rec.SessionID, TurnID: "t1", ALegID: cr.ALegID, BLegID: "b1",
			AttemptSeq: 1, StartedAt: time.Unix(1, 0),
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.AddUsage(ctx, domain.UsageDelta{
			SessionID: rec.SessionID, TurnID: "t1", BLegID: "b1",
			InputTokens: 3, OutputTokens: 5,
		}); err != nil {
			t.Fatal(err)
		}
		sums, err := s.Summary(ctx, domain.SummaryQuery{OwnerID: "owner-sum", WorkspaceID: "ws-sum", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		assertNonNilEmptySlice(t, sums)
		var found *domain.Summary
		for i := range sums {
			if sums[i].SessionID == rec.SessionID {
				found = &sums[i]
				break
			}
		}
		if found == nil {
			t.Fatalf("summary missing session: %#v", sums)
		}
		if found.AttemptCount < 1 {
			t.Fatalf("attempt count: got %d", found.AttemptCount)
		}
		if found.ALegID != "a-sum" || !found.ResumeEligible {
			t.Fatalf("summary lineage: %+v", found)
		}
		if found.PolicyVersion != "v1" || !found.TranscriptEnabled || found.AuditMode != "optional" {
			t.Fatalf("summary policy: %+v", found)
		}
		if found.UsageInputTokens != 3 || found.UsageOutputTokens != 5 {
			t.Fatalf("summary usage: %+v", found)
		}
	})

	t.Run("ListAttemptEvidence_trace_usage_outcome", func(t *testing.T) {
		s := newStore()
		fp, _ := twoFingerprints()
		cr := sampleCreate("owner-ev", "ws-ev", fp, "a-leg-ev", "sess-evidence")
		rec, err := s.Create(ctx, cr)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AppendAttemptTrace(ctx, domain.AttemptTrace{
			SessionID: rec.SessionID, TurnID: "t1", ALegID: cr.ALegID, BLegID: "b-ev",
			AttemptSeq: 1, RequestedModel: "m1", ResolvedBackend: "be1", ResolvedModel: "r1",
			RouteSource: "src1", StartedAt: time.Unix(10, 0),
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{
			SessionID: rec.SessionID, TurnID: "t1", BLegID: "b-ev", Success: true,
			SurfaceState: domain.SurfaceSurfaced, EndedAt: time.Unix(11, 0),
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.AddUsage(ctx, domain.UsageDelta{
			SessionID: rec.SessionID, TurnID: "t1", BLegID: "b-ev",
			InputTokens: 2, OutputTokens: 4, CacheReadTokens: 1,
		}); err != nil {
			t.Fatal(err)
		}
		ev, err := s.ListAttemptEvidence(ctx, rec.SessionID, domain.ReadOptions{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(ev) != 1 {
			t.Fatalf("want 1 attempt, got %d", len(ev))
		}
		if ev[0].Trace.BLegID != "b-ev" || ev[0].Trace.RequestedModel != "m1" {
			t.Fatalf("trace: %+v", ev[0].Trace)
		}
		if !ev[0].Outcome.Success || ev[0].Outcome.SurfaceState != domain.SurfaceSurfaced {
			t.Fatalf("outcome: %+v", ev[0].Outcome)
		}
		if ev[0].Accounting.InputTokens != 2 || ev[0].Accounting.OutputTokens != 4 || ev[0].Accounting.CacheReadTokens != 1 {
			t.Fatalf("accounting: %+v", ev[0].Accounting)
		}
	})

	t.Run("missing_lookups_non_enumerating", func(t *testing.T) {
		s := newStore()
		_, err := s.LoadByID(ctx, "no-such-session-id-xxxxxxxx")
		if !errors.Is(err, domain.ErrSessionNotFound) {
			t.Fatalf("LoadByID: %v", err)
		}
		var badFP domain.TokenFingerprint
		badFP[0] = 0xfe
		_, err = s.LoadByResumeFingerprint(ctx, badFP)
		if !errors.Is(err, domain.ErrSessionNotFound) {
			t.Fatalf("LoadByResumeFingerprint: %v", err)
		}
		_, err = s.LoadByALegID(ctx, "no-such-a-leg-zzzzzzzz")
		if !errors.Is(err, domain.ErrSessionNotFound) {
			t.Fatalf("LoadByALegID: %v", err)
		}
	})

	t.Run("TouchActivity_updates_record", func(t *testing.T) {
		s := newStore()
		fp, _ := twoFingerprints()
		rec, err := s.Create(ctx, sampleCreate("o", "w", fp, "a-touch", "sess-touch"))
		if err != nil {
			t.Fatal(err)
		}
		at := time.Unix(1700, 0)
		if err := s.TouchActivity(ctx, rec.SessionID, at, domain.ActivityRemoteEvent); err != nil {
			t.Fatal(err)
		}
		got, err := s.LoadByID(ctx, rec.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.LastActivityAt.Equal(at) || got.LastActivitySource != domain.ActivityRemoteEvent {
			t.Fatalf("touch: %+v", got)
		}
	})

	t.Run("TouchActivity_olderTimestampIgnored", func(t *testing.T) {
		s := newStore()
		fp, _ := twoFingerprints()
		rec, err := s.Create(ctx, sampleCreate("o", "w", fp, "a-touch-old", "sess-touch-old"))
		if err != nil {
			t.Fatal(err)
		}
		newer := time.Unix(5000, 0)
		if err := s.TouchActivity(ctx, rec.SessionID, newer, domain.ActivityRemoteEvent); err != nil {
			t.Fatal(err)
		}
		if err := s.TouchActivity(ctx, rec.SessionID, time.Unix(100, 0), domain.ActivityClientRequest); err != nil {
			t.Fatal(err)
		}
		got, err := s.LoadByID(ctx, rec.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.LastActivityAt.Equal(newer) || got.LastActivitySource != domain.ActivityRemoteEvent {
			t.Fatalf("monotonic touch: want %v remote got %+v", newer, got)
		}
	})

	t.Run("AppendTranscript_Audit_ordering", func(t *testing.T) {
		s := newStore()
		fp, _ := twoFingerprints()
		cr := sampleCreate("o", "w", fp, "a-audit", "sess-audit")
		cr.Policy.TranscriptEnabled = true
		rec, err := s.Create(ctx, cr)
		if err != nil {
			t.Fatal(err)
		}
		t0 := time.Unix(100, 0)
		if err := s.AppendTranscript(ctx, domain.TranscriptItem{
			SessionID: rec.SessionID, TurnID: "t1", Seq: 1, EventKind: "user", PayloadRef: "p1", CreatedAt: t0,
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendTranscript(ctx, domain.TranscriptItem{
			SessionID: rec.SessionID, TurnID: "t1", Seq: 2, EventKind: "assistant", PayloadRef: "p2", CreatedAt: t0.Add(time.Second),
		}); err != nil {
			t.Fatal(err)
		}
		tx, err := s.Transcript(ctx, rec.SessionID, domain.ReadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(tx) != 2 || tx[0].Seq != 1 || tx[1].Seq != 2 {
			t.Fatalf("transcript order: %#v", tx)
		}
		if err := s.AppendAudit(ctx, domain.AuditItem{
			SessionID: rec.SessionID, TurnID: "t1", Seq: 1, Action: "open", Result: "ok", CreatedAt: t0,
		}); err != nil {
			t.Fatal(err)
		}
		aud, err := s.Audit(ctx, rec.SessionID, domain.ReadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(aud) != 1 || aud[0].Action != "open" {
			t.Fatalf("audit: %#v", aud)
		}
	})

	t.Run("NextAuditSeq", func(t *testing.T) {
		s := newStore()
		fp, _ := twoFingerprints()
		cr := sampleCreate("o", "w", fp, "a-naudit", "sess-naudit")
		rec, err := s.Create(ctx, cr)
		if err != nil {
			t.Fatal(err)
		}
		n1, err := s.NextAuditSeq(ctx, rec.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if n1 != 1 {
			t.Fatalf("next: got %d want 1", n1)
		}
		if err := s.AppendAudit(ctx, domain.AuditItem{
			SessionID: rec.SessionID, TurnID: "t1", Seq: n1, Action: "a", Result: "r", CreatedAt: time.Unix(1, 0),
		}); err != nil {
			t.Fatal(err)
		}
		n2, err := s.NextAuditSeq(ctx, rec.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if n2 != 2 {
			t.Fatalf("next: got %d want 2", n2)
		}
	})

	t.Run("NextTranscriptSeq", func(t *testing.T) {
		s := newStore()
		fp, _ := twoFingerprints()
		cr := sampleCreate("o", "w", fp, "a-ntr", "sess-ntr")
		cr.Policy.TranscriptEnabled = true
		rec, err := s.Create(ctx, cr)
		if err != nil {
			t.Fatal(err)
		}
		n1, err := s.NextTranscriptSeq(ctx, rec.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if n1 != 1 {
			t.Fatalf("next transcript: got %d want 1", n1)
		}
		if err := s.AppendTranscript(ctx, domain.TranscriptItem{
			SessionID: rec.SessionID, TurnID: "t1", Seq: n1, EventKind: "x", PayloadRef: "{}", CreatedAt: time.Unix(1, 0),
		}); err != nil {
			t.Fatal(err)
		}
		n2, err := s.NextTranscriptSeq(ctx, rec.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if n2 != 2 {
			t.Fatalf("next transcript: got %d want 2", n2)
		}
	})
}

func assertNonNilEmptySlice[T any](t *testing.T, s []T) {
	t.Helper()
	if s == nil {
		t.Fatal("expected non-nil slice (empty ok)")
	}
}

func twoFingerprints() (domain.TokenFingerprint, domain.TokenFingerprint) {
	var a, b domain.TokenFingerprint
	a[0] = 1
	a[31] = 2
	b[0] = 2
	b[31] = 3
	return a, b
}

func sampleCreate(owner, ws string, fp domain.TokenFingerprint, aLeg string, sid domain.SessionID) domain.CreateRecord {
	return domain.CreateRecord{
		SessionID:         sid,
		ResumeFingerprint: fp,
		Owner:             domain.PrincipalRef{ID: owner},
		Workspace:         domain.WorkspaceRef{ID: ws},
		ClientHints:       domain.ClientHints{ClientSessionID: "hint"},
		Policy: domain.PolicyMetadata{
			PolicyVersion: "v1", TranscriptEnabled: true, AuditMode: "optional",
		},
		ALegID:         aLeg,
		ResumeEligible: true,
		CreatedAt:      time.Unix(1, 0),
	}
}
