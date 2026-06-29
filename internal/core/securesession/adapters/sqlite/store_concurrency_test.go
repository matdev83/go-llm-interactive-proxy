//go:build integration

package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
)

func TestStore_concurrentLoadByResumeFingerprint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "fp-conc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	fp := domain.TokenFingerprint{}
	fp[0] = 0x77
	fp[31] = 0x88
	_, err = s.Create(ctx, domain.CreateRecord{
		SessionID: "sess-fp-read", ResumeFingerprint: fp,
		Owner: domain.PrincipalRef{ID: "o"}, Workspace: domain.WorkspaceRef{ID: "w"},
		Policy: domain.PolicyMetadata{PolicyVersion: "1", TranscriptEnabled: false},
		ALegID: "aleg-fp", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}

	const readers = 128
	var wg sync.WaitGroup
	var mu sync.Mutex
	errs := make([]error, 0, readers)
	wg.Add(readers)
	for range readers {
		go func() {
			defer wg.Done()
			got, err := s.LoadByResumeFingerprint(ctx, fp)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			if got.SessionID != "sess-fp-read" {
				mu.Lock()
				errs = append(errs, fmt.Errorf("session id %q", got.SessionID))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		t.Error(err)
	}
}

func TestStore_concurrentTouchActivity_monotonicFakeClock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "touch-mono.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	fp := domain.TokenFingerprint{}
	fp[1] = 0x22
	_, err = s.Create(ctx, domain.CreateRecord{
		SessionID: "sess-mono", ResumeFingerprint: fp,
		Owner: domain.PrincipalRef{ID: "o"}, Workspace: domain.WorkspaceRef{ID: "w"},
		Policy: domain.PolicyMetadata{PolicyVersion: "1"},
		ALegID: "a-mono", ResumeEligible: true, CreatedAt: time.Unix(10, 0),
	})
	if err != nil {
		t.Fatal(err)
	}

	tLow := time.Unix(100, 0)
	tMid := time.Unix(300, 0)
	tMax := time.Unix(500, 0)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		_ = s.TouchActivity(ctx, "sess-mono", tMax, domain.ActivityRemoteEvent)
	}()
	go func() {
		defer wg.Done()
		_ = s.TouchActivity(ctx, "sess-mono", tLow, domain.ActivityClientRequest)
	}()
	go func() {
		defer wg.Done()
		_ = s.TouchActivity(ctx, "sess-mono", tMid, domain.ActivityRemoteEvent)
	}()
	wg.Wait()

	got, err := s.LoadByID(ctx, "sess-mono")
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastActivityAt.Equal(tMax) {
		t.Fatalf("LastActivityAt want %v got %v (monotonic merge)", tMax, got.LastActivityAt)
	}
}

func TestStore_concurrentAttemptTraceOutcomeUsageTranscript(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "trace-conc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	fp := domain.TokenFingerprint{}
	fp[3] = 0x44
	_, err = s.Create(ctx, domain.CreateRecord{
		SessionID: "sess-trace-mix", ResumeFingerprint: fp,
		Owner: domain.PrincipalRef{ID: "o"}, Workspace: domain.WorkspaceRef{ID: "w"},
		Policy: domain.PolicyMetadata{PolicyVersion: "1", TranscriptEnabled: true},
		ALegID: "a-mix", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 24
	var wg sync.WaitGroup
	var mu sync.Mutex
	errs := make([]error, 0, workers)
	appendErr := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}
	wg.Add(workers)
	for i := range workers {
		go func(i int) {
			defer wg.Done()
			bLeg := "b-leg-" + strconv.Itoa(i)
			turnID := domain.TurnID("turn-" + strconv.Itoa(i))
			t0 := time.Unix(100, int64(i))
			if err := s.AppendAttemptTrace(ctx, domain.AttemptTrace{
				SessionID: "sess-trace-mix", TurnID: turnID, ALegID: "a-mix", BLegID: bLeg,
				AttemptSeq: 1, RequestedModel: "m", ResolvedBackend: "be", ResolvedModel: "rm",
				StartedAt: t0,
			}); err != nil {
				appendErr(err)
				return
			}
			if err := s.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{
				SessionID: "sess-trace-mix", TurnID: turnID, BLegID: bLeg, Success: i%2 == 0,
				SurfaceState: domain.SurfaceSurfaced, EndedAt: t0.Add(time.Second),
			}); err != nil {
				appendErr(err)
				return
			}
			if err := s.AddUsage(ctx, domain.UsageDelta{
				SessionID: "sess-trace-mix", TurnID: turnID, BLegID: bLeg,
				InputTokens: int64(i + 1), OutputTokens: int64(i + 2),
			}); err != nil {
				appendErr(err)
			}
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		t.Error(err)
	}

	// Sequential transcript writes after concurrent stress: seq is allocated inside AppendTranscript
	// under a session row lock, so ordering stays deterministic here.
	for i := range workers {
		turnID := domain.TurnID("turn-" + strconv.Itoa(i))
		t0 := time.Unix(100, int64(i))
		seq, err := s.NextTranscriptSeq(ctx, "sess-trace-mix")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AppendTranscript(ctx, domain.TranscriptItem{
			SessionID: "sess-trace-mix", TurnID: turnID, Seq: seq,
			EventKind: "evt", PayloadRef: "p" + strconv.Itoa(i), CreatedAt: t0,
		}); err != nil {
			t.Fatal(err)
		}
	}

	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM lip_secure_attempt_traces WHERE session_id = ?`, "sess-trace-mix").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != workers {
		t.Fatalf("attempt trace rows: want %d got %d", workers, n)
	}

	// Sequential tail writer: latest snapshot fields must stay self-consistent after concurrent mix.
	tFinal := time.Unix(999_999, 0)
	turnFinal := domain.TurnID("turn-final")
	if err := s.AppendAttemptTrace(ctx, domain.AttemptTrace{
		SessionID: "sess-trace-mix", TurnID: turnFinal, ALegID: "a-mix", BLegID: "b-final",
		AttemptSeq: 1, RequestedModel: "mf", ResolvedBackend: "bef", ResolvedModel: "rmf",
		StartedAt: tFinal,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAttemptOutcome(ctx, domain.AttemptOutcome{
		SessionID: "sess-trace-mix", TurnID: turnFinal, BLegID: "b-final", Success: true,
		SurfaceState: domain.SurfaceSurfaced, EndedAt: tFinal.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(ctx, domain.UsageDelta{
		SessionID: "sess-trace-mix", TurnID: turnFinal, BLegID: "b-final",
		InputTokens: 99, OutputTokens: 101,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.LoadByID(ctx, "sess-trace-mix")
	if err != nil {
		t.Fatal(err)
	}
	if got.LatestAttemptTrace.BLegID != "b-final" || got.LatestAttemptOutcome.BLegID != "b-final" {
		t.Fatalf("want b-final traces outcome=%+v trace=%+v", got.LatestAttemptOutcome, got.LatestAttemptTrace)
	}
	if got.LatestAttemptAccounting.BLegID != "b-final" {
		t.Fatalf("want b-final accounting %+v", got.LatestAttemptAccounting)
	}
	tx, err := s.Transcript(ctx, "sess-trace-mix", domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tx) != workers {
		t.Fatalf("transcript rows want %d got %d", workers, len(tx))
	}
}
