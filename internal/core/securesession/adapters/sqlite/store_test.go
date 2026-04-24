package sqlite

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
)

func TestStore_restartSurvival(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "secure.db")

	var s1 *Store
	defer func() {
		if s1 != nil {
			_ = s1.Close()
		}
	}()

	var err error
	s1, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}

	fp := domain.TokenFingerprint{}
	fp[0] = 0xab
	fp[31] = 0xcd

	cr := domain.CreateRecord{
		SessionID:         "sess-restart-1",
		ResumeFingerprint: fp,
		Owner:             domain.PrincipalRef{ID: "owner-r", Issuer: "iss", Tenant: "ten"},
		Workspace:         domain.WorkspaceRef{ID: "ws-r"},
		ClientHints:       domain.ClientHints{ClientSessionID: "hint-r", AgentIdentityDigest: "dig"},
		Policy: domain.PolicyMetadata{
			PolicyVersion: "pv1", TranscriptEnabled: true, EffectiveTreatment: "strict",
			StricterPolicyResolution: "win", RouteHint: "rh", RedactionProfile: "rp", AuditMode: "optional",
		},
		ALegID:         "a-leg-restart",
		ResumeEligible: true,
		CreatedAt:      time.Unix(100, 0),
	}
	rec, err := s1.Create(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.TouchActivity(ctx, rec.SessionID, time.Unix(200, 0), domain.ActivityRemoteEvent); err != nil {
		t.Fatal(err)
	}
	if err := s1.AppendAttemptTrace(ctx, domain.AttemptTrace{
		SessionID: rec.SessionID, TurnID: "t1", ALegID: cr.ALegID, BLegID: "b-restart",
		AttemptSeq: 1, RequestedModel: "m1", ResolvedBackend: "be1", ResolvedModel: "rm1",
		RouteSource: "src", RouteReason: "why", StartedAt: time.Unix(300, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s1.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{
		SessionID: rec.SessionID, TurnID: "t1", BLegID: "b-restart", Success: true,
		SurfaceState: domain.SurfaceSurfaced, EndedAt: time.Unix(301, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s1.AddUsage(ctx, domain.UsageDelta{
		SessionID: rec.SessionID, TurnID: "t1", BLegID: "b-restart",
		InputTokens: 7, OutputTokens: 11,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s1.AppendTranscript(ctx, domain.TranscriptItem{
		SessionID: rec.SessionID, TurnID: "t1", Seq: 1, EventKind: "user", PayloadRef: "p1", CreatedAt: time.Unix(400, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s1.AppendAudit(ctx, domain.AuditItem{
		SessionID: rec.SessionID, TurnID: "t1", Seq: 1, Action: "act", Result: "res", CreatedAt: time.Unix(500, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	s1 = nil

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()

	byID, err := s2.LoadByID(ctx, rec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if byID.Owner.ID != "owner-r" || byID.Owner.Issuer != "iss" || byID.Owner.Tenant != "ten" {
		t.Fatalf("owner: %+v", byID.Owner)
	}
	if byID.Workspace.ID != "ws-r" {
		t.Fatalf("workspace: %+v", byID.Workspace)
	}
	if byID.Policy.PolicyVersion != "pv1" || !byID.Policy.TranscriptEnabled {
		t.Fatalf("policy: %+v", byID.Policy)
	}
	if byID.ALegID != "a-leg-restart" || !byID.ResumeEligible {
		t.Fatalf("a-leg / resume: %+v", byID)
	}
	if !byID.LastActivityAt.Equal(time.Unix(200, 0)) || byID.LastActivitySource != domain.ActivityRemoteEvent {
		t.Fatalf("activity: %+v", byID)
	}
	if byID.LatestAttemptTrace.BLegID != "b-restart" || byID.LatestAttemptOutcome.Success != true {
		t.Fatalf("attempt: trace=%+v outcome=%+v", byID.LatestAttemptTrace, byID.LatestAttemptOutcome)
	}
	if byID.LatestAttemptAccounting.InputTokens != 7 || byID.LatestAttemptAccounting.OutputTokens != 11 {
		t.Fatalf("accounting: %+v", byID.LatestAttemptAccounting)
	}

	byFP, err := s2.LoadByResumeFingerprint(ctx, fp)
	if err != nil {
		t.Fatal(err)
	}
	if byFP.SessionID != rec.SessionID {
		t.Fatalf("fingerprint load: %v", byFP.SessionID)
	}
	byALeg, err := s2.LoadByALegID(ctx, "a-leg-restart")
	if err != nil {
		t.Fatal(err)
	}
	if byALeg.SessionID != rec.SessionID {
		t.Fatalf("a-leg load: %v", byALeg.SessionID)
	}
	sums, err := s2.Summary(ctx, domain.SummaryQuery{OwnerID: "owner-r", WorkspaceID: "ws-r", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 1 {
		t.Fatalf("summary count %d", len(sums))
	}
	if sums[0].TurnCount < 1 || sums[0].AttemptCount < 1 {
		t.Fatalf("summary rollups: %+v", sums[0])
	}
	if !sums[0].ResumeEligible || sums[0].ALegID != "a-leg-restart" || sums[0].PolicyVersion != "pv1" {
		t.Fatalf("summary lineage/policy: %+v", sums[0])
	}
	if !sums[0].TranscriptEnabled || sums[0].RedactionProfile != "rp" || sums[0].AuditMode != "optional" {
		t.Fatalf("summary policy flags: %+v", sums[0])
	}
	if sums[0].UsageInputTokens != 7 || sums[0].UsageOutputTokens != 11 {
		t.Fatalf("summary usage: %+v", sums[0])
	}
	tx, err := s2.Transcript(ctx, rec.SessionID, domain.ReadOptions{})
	if err != nil || len(tx) != 1 {
		t.Fatalf("transcript: err=%v len=%d", err, len(tx))
	}
	aud, err := s2.Audit(ctx, rec.SessionID, domain.ReadOptions{})
	if err != nil || len(aud) != 1 {
		t.Fatalf("audit: err=%v len=%d", err, len(aud))
	}
}

func TestStore_concurrentCreateAndTouch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "conc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	const workers = 32
	var wg sync.WaitGroup
	wg.Add(workers)
	baseFP := domain.TokenFingerprint{}
	baseFP[1] = 0x11
	for i := range workers {
		go func(i int) {
			defer wg.Done()
			fp := baseFP
			fp[0] = byte(i)
			fp[15] = byte(i >> 8)
			sid := domain.SessionID("sess-conc-" + strconv.Itoa(i))
			aLeg := "aleg-" + strconv.Itoa(i)
			_, err := s.Create(ctx, domain.CreateRecord{
				SessionID:         sid,
				ResumeFingerprint: fp,
				Owner:             domain.PrincipalRef{ID: "co"},
				Workspace:         domain.WorkspaceRef{ID: "w"},
				Policy: domain.PolicyMetadata{
					PolicyVersion: "v", TranscriptEnabled: false, AuditMode: "optional",
				},
				ALegID:         aLeg,
				ResumeEligible: true,
				CreatedAt:      time.Unix(1, 0),
			})
			if err != nil {
				t.Error(err)
				return
			}
			at := time.Unix(1000+int64(i), 0)
			if err := s.TouchActivity(ctx, sid, at, domain.ActivityClientRequest); err != nil {
				t.Error(err)
				return
			}
			if err := s.AppendAttemptTrace(ctx, domain.AttemptTrace{
				SessionID: sid, TurnID: "t", ALegID: aLeg, BLegID: "b",
				AttemptSeq: 1, StartedAt: at,
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	for i := range workers {
		sid := domain.SessionID("sess-conc-" + strconv.Itoa(i))
		got, err := s.LoadByID(ctx, sid)
		if err != nil {
			t.Fatalf("load %s: %v", sid, err)
		}
		if got.LastActivitySource != domain.ActivityClientRequest {
			t.Fatalf("session %s source %s", sid, got.LastActivitySource)
		}
	}
}

func TestAppendAttemptTrace_twoRowsSameSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "two-trace.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	fp := domain.TokenFingerprint{}
	fp[0] = 1
	_, err = s.Create(ctx, domain.CreateRecord{
		SessionID: "sess-2trace", ResumeFingerprint: fp,
		Owner: domain.PrincipalRef{ID: "o"}, Workspace: domain.WorkspaceRef{},
		ClientHints: domain.ClientHints{}, Policy: domain.PolicyMetadata{PolicyVersion: "1"},
		ALegID: "a2", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	base := domain.AttemptTrace{
		SessionID: "sess-2trace", TurnID: "t1", ALegID: "a2", StartedAt: time.Unix(2, 0),
	}
	t1 := base
	t1.BLegID, t1.AttemptSeq, t1.ResolvedModel = "b1", 1, "m1"
	if err := s.AppendAttemptTrace(ctx, t1); err != nil {
		t.Fatal(err)
	}
	t2 := base
	t2.BLegID, t2.AttemptSeq, t2.ResolvedModel = "b2", 2, "m2"
	if err := s.AppendAttemptTrace(ctx, t2); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM lip_secure_attempt_traces WHERE session_id = ?`, "sess-2trace").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("trace rows: want 2 got %d", n)
	}
}

func TestStore_perAttemptOutcomesDistinctBLEGs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "outcomes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	fp := domain.TokenFingerprint{}
	fp[1] = 2
	_, err = s.Create(ctx, domain.CreateRecord{
		SessionID: "sess-out", ResumeFingerprint: fp,
		Owner: domain.PrincipalRef{ID: "o"}, Workspace: domain.WorkspaceRef{},
		ClientHints: domain.ClientHints{}, Policy: domain.PolicyMetadata{PolicyVersion: "1"},
		ALegID: "a-out", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	base := domain.AttemptTrace{SessionID: "sess-out", TurnID: "t1", ALegID: "a-out", StartedAt: time.Unix(2, 0)}
	t1 := base
	t1.BLegID, t1.AttemptSeq, t1.ResolvedModel = "bleg-1", 1, "m1"
	if err := s.AppendAttemptTrace(ctx, t1); err != nil {
		t.Fatal(err)
	}
	t2 := base
	t2.BLegID, t2.AttemptSeq, t2.ResolvedModel = "bleg-2", 2, "m2"
	if err := s.AppendAttemptTrace(ctx, t2); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{
		SessionID: "sess-out", TurnID: "t1", BLegID: "bleg-1", Success: false,
		SurfaceState: domain.SurfaceSwallowed, EndedAt: time.Unix(10, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{
		SessionID: "sess-out", TurnID: "t1", BLegID: "bleg-2", Success: true,
		SurfaceState: domain.SurfaceSurfaced, EndedAt: time.Unix(20, 0),
	}); err != nil {
		t.Fatal(err)
	}
	var surf1, surf2 string
	var end1, end2 int64
	err = s.db.QueryRowContext(ctx,
		`SELECT surface_state, ended_at_unix FROM lip_secure_attempt_traces WHERE session_id = ? AND b_leg_id = ?`,
		"sess-out", "bleg-1").Scan(&surf1, &end1)
	if err != nil {
		t.Fatal(err)
	}
	err = s.db.QueryRowContext(ctx,
		`SELECT surface_state, ended_at_unix FROM lip_secure_attempt_traces WHERE session_id = ? AND b_leg_id = ?`,
		"sess-out", "bleg-2").Scan(&surf2, &end2)
	if err != nil {
		t.Fatal(err)
	}
	if surf1 != string(domain.SurfaceSwallowed) || end1 != time.Unix(10, 0).UnixNano() {
		t.Fatalf("b1 row: surface=%q end=%d", surf1, end1)
	}
	if surf2 != string(domain.SurfaceSurfaced) || end2 != time.Unix(20, 0).UnixNano() {
		t.Fatalf("b2 row: surface=%q end=%d", surf2, end2)
	}
}
