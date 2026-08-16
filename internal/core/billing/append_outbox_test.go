package billing

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryingCallUsageAppenderEnqueuesAfterAppendFailure(t *testing.T) {
	appendErr := errors.New("temporary append I/O")
	var got CallUsageRecord
	var reason string
	appender, err := NewRetryingCallUsageAppender(
		CallUsageAppenderFunc(func(context.Context, CallUsageRecord) error { return appendErr }),
		usageAppendOutboxFunc{
			enqueueCall: func(_ context.Context, record CallUsageRecord, cause string) error {
				got = record
				reason = cause
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	record := testAppendOutboxCallRecord(t)
	if err := appender.AppendCallUsage(context.Background(), record); !errors.Is(err, appendErr) {
		t.Fatalf("AppendCallUsage error = %v, want %v", err, appendErr)
	}
	if got.CallID != record.CallID {
		t.Fatalf("queued call = %+v, want %+v", got, record)
	}
	if reason == "" {
		t.Fatal("outbox enqueue reason is empty")
	}
}

func TestUsageAppendWorkerReplaysCallAndLegWithoutProviderExecution(t *testing.T) {
	call := testAppendOutboxCallRecord(t)
	leg := testAppendOutboxLegRecord(t, call.CallID)
	outbox := &usageAppendOutboxMemory{work: []UsageAppendWork{
		{Key: "call-key", Kind: UsageAppendCall, Call: &call},
		{Key: "leg-key", Kind: UsageAppendLeg, Leg: &leg},
	}}
	var calls, legs int
	worker, err := NewUsageAppendWorker(
		outbox,
		CallUsageAppenderFunc(func(context.Context, CallUsageRecord) error { calls++; return nil }),
		CallLegUsageAppenderFunc(func(context.Context, CallLegUsageRecord) error { legs++; return nil }),
		2,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || legs != 1 {
		t.Fatalf("replayed calls/legs = %d/%d, want 1/1", calls, legs)
	}
	if len(outbox.processed) != 2 {
		t.Fatalf("processed keys = %v, want 2", outbox.processed)
	}
}

func TestUsageAppendWorkerDefersTransientFailure(t *testing.T) {
	call := testAppendOutboxCallRecord(t)
	outbox := &usageAppendOutboxMemory{work: []UsageAppendWork{{Key: "call-key", Kind: UsageAppendCall, Call: &call}}}
	appendErr := errors.New("database busy")
	worker, err := NewUsageAppendWorker(
		outbox,
		CallUsageAppenderFunc(func(context.Context, CallUsageRecord) error { return appendErr }),
		CallLegUsageAppenderFunc(func(context.Context, CallLegUsageRecord) error { return nil }),
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(context.Background()); !errors.Is(err, appendErr) {
		t.Fatalf("ProcessOnce error = %v, want %v", err, appendErr)
	}
	if len(outbox.deferred) != 1 {
		t.Fatalf("deferred keys = %v, want one", outbox.deferred)
	}
}

func testAppendOutboxCallRecord(t *testing.T) CallUsageRecord {
	t.Helper()
	callID, err := NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	return CallUsageRecord{CallID: callID, AccountID: "acct", ALegID: "a1", SessionID: "s1", StartedAt: now, FinishedAt: now.Add(time.Second), Outcome: TurnOutcomeCompleted, ExpectedBLegIDs: []string{"b1"}}
}

func testAppendOutboxLegRecord(t *testing.T, callID BillingCallID) CallLegUsageRecord {
	t.Helper()
	now := time.Unix(100, 0).UTC()
	return CallLegUsageRecord{CallID: callID, ALegID: "a1", BLegID: "b1", BackendID: "backend", ProviderID: "provider", ModelID: "model", StartedAt: now, FinishedAt: now.Add(time.Second), Outcome: LegOutcomeWinner, Surfaced: SurfacedYes}
}

type usageAppendOutboxFunc struct {
	enqueueCall func(context.Context, CallUsageRecord, string) error
}

func (f usageAppendOutboxFunc) EnqueueCallUsageAppend(ctx context.Context, record CallUsageRecord, cause string) error {
	if f.enqueueCall == nil {
		return nil
	}
	return f.enqueueCall(ctx, record, cause)
}
func (usageAppendOutboxFunc) EnqueueCallLegUsageAppend(context.Context, CallLegUsageRecord, string) error {
	return nil
}
func (usageAppendOutboxFunc) ListPendingUsageAppendWork(context.Context, int) ([]UsageAppendWork, error) {
	return nil, nil
}
func (usageAppendOutboxFunc) MarkUsageAppendProcessed(context.Context, string) error { return nil }
func (usageAppendOutboxFunc) DeferUsageAppend(context.Context, string, string) error { return nil }
func (usageAppendOutboxFunc) FailUsageAppend(context.Context, string, string) error  { return nil }

type usageAppendOutboxMemory struct {
	work      []UsageAppendWork
	processed []string
	deferred  []string
}

func (m *usageAppendOutboxMemory) EnqueueCallUsageAppend(context.Context, CallUsageRecord, string) error {
	return nil
}
func (m *usageAppendOutboxMemory) EnqueueCallLegUsageAppend(context.Context, CallLegUsageRecord, string) error {
	return nil
}
func (m *usageAppendOutboxMemory) ListPendingUsageAppendWork(context.Context, int) ([]UsageAppendWork, error) {
	work := m.work
	m.work = nil
	return work, nil
}
func (m *usageAppendOutboxMemory) MarkUsageAppendProcessed(_ context.Context, key string) error {
	m.processed = append(m.processed, key)
	return nil
}
func (m *usageAppendOutboxMemory) DeferUsageAppend(_ context.Context, key, _ string) error {
	m.deferred = append(m.deferred, key)
	return nil
}
func (m *usageAppendOutboxMemory) FailUsageAppend(context.Context, string, string) error { return nil }
