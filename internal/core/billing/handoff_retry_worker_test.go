package billing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestHandoffRetryWorkerAppendsSealedTURFromOutbox(t *testing.T) {
	t.Parallel()
	var got TurnUsageRecord
	appender := UsageRecordAppenderFunc(func(_ context.Context, record TurnUsageRecord) error {
		got = record
		return nil
	})
	outbox := NewMemoryHandoffOutbox()
	worker, err := NewHandoffRetryWorker(outbox, appender, nil, HandoffRetryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	job := testHandoffJob(testHandoffLeg("b-1", 1))
	if err := outbox.Enqueue(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.AccountID != "acct" || got.ALegID != "a-1" || got.AuthorizationID != "auth" {
		t.Fatalf("appended identity = %+v", got)
	}
	if len(got.Legs) != 1 || got.Legs[0].BLegID != "b-1" {
		t.Fatalf("appended legs = %+v", got.Legs)
	}
	pending, err := outbox.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("pending after success = %d", pending)
	}
}

func TestHandoffRetryWorkerRetriesAppendFailureWithoutDroppingLegs(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var calls int
	appender := UsageRecordAppenderFunc(func(context.Context, TurnUsageRecord) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return errors.New("durable unavailable")
		}
		return nil
	})
	outbox := NewMemoryHandoffOutbox()
	worker, err := NewHandoffRetryWorker(outbox, appender, nil, HandoffRetryConfig{RetryDelay: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.Enqueue(context.Background(), testHandoffJob(testHandoffLeg("b-1", 1))); err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	pending, err := outbox.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("failed append must keep the job, pending=%d", pending)
	}
	time.Sleep(time.Millisecond)
	if err := worker.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 2 {
		t.Fatalf("append calls = %d, want 2", n)
	}
	pending, err = outbox.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("pending after retry success = %d", pending)
	}
}

func TestHandoffRetryWorkerReleasesHoldWhenNoEvidenceExhaustedBeforeOpen(t *testing.T) {
	t.Parallel()
	var released ReleaseAuthorizationInput
	releaser := HoldReleaserFunc(func(_ context.Context, in ReleaseAuthorizationInput) (Posting, error) {
		released = in
		return Posting{}, nil
	})
	outbox := NewMemoryHandoffOutbox()
	worker, err := NewHandoffRetryWorker(outbox, UsageRecordAppenderFunc(func(context.Context, TurnUsageRecord) error {
		t.Fatal("append must not run without legs")
		return nil
	}), releaser, HandoffRetryConfig{NoEvidenceMaxAttempts: 2, RetryDelay: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	job := testHandoffJob()
	job.UpstreamOpened = false
	if err := outbox.Enqueue(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessUntilIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if released.Reason != ReleaseExecutionNotStarted {
		t.Fatalf("release reason = %q, want %q", released.Reason, ReleaseExecutionNotStarted)
	}
	if released.AuthorizationID != "auth" {
		t.Fatalf("release auth = %q", released.AuthorizationID)
	}
}

func TestHandoffRetryWorkerRetainsHoldAfterOpenWithoutEvidence(t *testing.T) {
	t.Parallel()
	var releases int
	releaser := HoldReleaserFunc(func(context.Context, ReleaseAuthorizationInput) (Posting, error) {
		releases++
		return Posting{}, nil
	})
	outbox := NewMemoryHandoffOutbox()
	worker, err := NewHandoffRetryWorker(outbox, nil, releaser, HandoffRetryConfig{NoEvidenceMaxAttempts: 2, RetryDelay: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	job := testHandoffJob()
	job.UpstreamOpened = true
	if err := outbox.Enqueue(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessUntilIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if releases != 0 {
		t.Fatalf("releases = %d, want 0", releases)
	}
	pending, err := outbox.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("exhausted open-without-evidence must complete without release, pending=%d", pending)
	}
}

func TestHandoffRetryWorkerDoesNotAppendWhileBarrierPending(t *testing.T) {
	t.Parallel()
	var calls int
	appender := UsageRecordAppenderFunc(func(context.Context, TurnUsageRecord) error {
		calls++
		return nil
	})
	outbox := NewMemoryHandoffOutbox()
	worker, err := NewHandoffRetryWorker(outbox, appender, nil, HandoffRetryConfig{RetryDelay: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	job := testHandoffJob(testHandoffLeg("b-winner", 1))
	job.BarrierPending = true
	if err := outbox.Enqueue(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("append calls = %d, want 0 while barrier pending", calls)
	}
	if err := outbox.MarkBarrierComplete(context.Background(), "a-1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := worker.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("append calls after barrier = %d, want 1", calls)
	}
}

func TestHandoffRetryWorkerMergeLegsUpdatesPendingJob(t *testing.T) {
	t.Parallel()
	var got TurnUsageRecord
	appender := UsageRecordAppenderFunc(func(_ context.Context, record TurnUsageRecord) error {
		got = record
		return nil
	})
	outbox := NewMemoryHandoffOutbox()
	worker, err := NewHandoffRetryWorker(outbox, appender, nil, HandoffRetryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	job := testHandoffJob(testHandoffLeg("b-1", 1))
	job.BarrierPending = true
	if err := outbox.Enqueue(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := outbox.MergeLegs(context.Background(), "a-1", []LegUsageRecord{testHandoffLeg("b-2", 2)}); err != nil {
		t.Fatal(err)
	}
	if err := outbox.MarkBarrierComplete(context.Background(), "a-1"); err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got.Legs) != 2 || got.Legs[0].BLegID != "b-1" || got.Legs[1].BLegID != "b-2" {
		t.Fatalf("merged legs = %+v", got.Legs)
	}
}

func TestHandoffRetryWorkerBackoffDoublesUntilCap(t *testing.T) {
	t.Parallel()
	worker, err := NewHandoffRetryWorker(NewMemoryHandoffOutbox(), nil, nil, HandoffRetryConfig{
		RetryDelay: 100 * time.Millisecond, MaxRetryDelay: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d := worker.retryAfter(HandoffRetryJob{NoEvidenceAttempts: 1}); d != 100*time.Millisecond {
		t.Fatalf("first backoff = %s, want 100ms", d)
	}
	if d := worker.retryAfter(HandoffRetryJob{NoEvidenceAttempts: 2}); d != 200*time.Millisecond {
		t.Fatalf("second backoff = %s, want 200ms", d)
	}
	if d := worker.retryAfter(HandoffRetryJob{EvidenceAttempts: 10}); d != 5*time.Second {
		t.Fatalf("capped backoff = %s, want 5s", d)
	}
}

func testHandoffJob(legs ...LegUsageRecord) HandoffRetryJob {
	return HandoffRetryJob{
		AccountID: "acct", AuthorizationID: "auth", ALegID: "a-1",
		Outcome: TurnOutcomeCompleted, Legs: legs,
	}
}

func testHandoffLeg(bLegID string, seq int) LegUsageRecord {
	return LegUsageRecord{
		ALegID: "a-1", BLegID: bLegID, Seq: seq,
		BackendID: "backend", ProviderID: "provider", ModelID: "model",
		StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(),
		Outcome: LegOutcomeWinner, Surfaced: SurfacedYes,
		Evidence: FinalBillingEvidence{InputTokens: Quantity{Value: 1, Present: true}},
	}
}
