package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteUsageAppendOutboxRecoversFailedCallAppend(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	call := testOutboxCall(t)
	appendErr := errors.New("temporary database I/O")
	first := true
	appender, err := billing.NewRetryingCallUsageAppender(
		billing.CallUsageAppenderFunc(func(context.Context, billing.CallUsageRecord) error {
			if first {
				first = false
				return appendErr
			}
			return store.AppendCallUsage(ctx, call)
		}),
		store,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := appender.AppendCallUsage(ctx, call); !errors.Is(err, appendErr) {
		t.Fatalf("initial append error = %v, want %v", err, appendErr)
	}
	work, err := store.ListPendingUsageAppendWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 1 || work[0].Kind != billing.UsageAppendCall {
		t.Fatalf("pending work = %+v, want one call append", work)
	}

	worker, err := billing.NewUsageAppendWorker(store, store, store, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetCallUsage(ctx, call.CallID); err != nil {
		t.Fatalf("replayed call usage: %v", err)
	}
	work, err = store.ListPendingUsageAppendWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 0 {
		t.Fatalf("pending work after replay = %+v, want empty", work)
	}
}

func TestSQLiteUsageAppendOutboxListsCallAndLegAndDefersDueWork(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	call := testOutboxCall(t)
	leg := testOutboxLeg(t, call.CallID)
	if err := store.EnqueueCallUsageAppend(ctx, call, "call I/O"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueCallLegUsageAppend(ctx, leg, "leg I/O"); err != nil {
		t.Fatal(err)
	}
	work, err := store.ListPendingUsageAppendWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 2 || work[0].Call == nil || work[1].Leg == nil {
		t.Fatalf("decoded work = %+v, want call and leg", work)
	}
	deferredKey := work[0].Key
	if err := store.DeferUsageAppend(ctx, deferredKey, "busy"); err != nil {
		t.Fatal(err)
	}
	work, err = store.ListPendingUsageAppendWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 1 {
		t.Fatalf("deferred work visible immediately = %+v, want one", work)
	}
	var attempts int
	var next time.Time
	if err := store.db.NewRaw(`SELECT attempt_count, next_attempt_at FROM usage_append_outbox WHERE append_key = ?`, deferredKey).Scan(ctx, &attempts, &next); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || next.IsZero() {
		t.Fatalf("deferred work state = attempts %d next %v", attempts, next)
	}
	if _, err := store.db.NewRaw(`UPDATE usage_append_outbox SET next_attempt_at = ? WHERE append_key = ?`, time.Now().UTC().Add(-time.Second), deferredKey).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	work, err = store.ListPendingUsageAppendWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 2 {
		t.Fatalf("due work = %+v, want two", work)
	}
}

func TestSQLiteUsageAppendOutboxMarksReplayConflictTerminal(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	call := testOutboxCall(t)
	if err := store.EnqueueCallUsageAppend(ctx, call, "conflict"); err != nil {
		t.Fatal(err)
	}
	if err := store.FailUsageAppend(ctx, mustCallKey(t, call), "fingerprint conflict"); err != nil {
		t.Fatal(err)
	}
	work, err := store.ListPendingUsageAppendWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 0 {
		t.Fatalf("failed work = %+v, want hidden", work)
	}
	var status string
	if err := store.db.NewRaw(`SELECT status FROM usage_append_outbox WHERE append_key = ?`, mustCallKey(t, call)).Scan(ctx, &status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("outbox status = %q, want failed", status)
	}
}

func testOutboxCall(t *testing.T) billing.CallUsageRecord {
	t.Helper()
	id, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	return billing.CallUsageRecord{SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: id, AccountID: "outbox-acct", ALegID: "a-outbox", SessionID: "s-outbox", StartedAt: now, FinishedAt: now.Add(time.Second), Outcome: billing.TurnOutcomeCompleted, ExpectedBLegIDs: []string{"b-outbox"}}
}

func testOutboxLeg(t *testing.T, callID billing.BillingCallID) billing.CallLegUsageRecord {
	t.Helper()
	now := time.Unix(100, 0).UTC()
	return billing.CallLegUsageRecord{CallID: callID, ALegID: "a-outbox", BLegID: "b-outbox", BackendID: "backend", ProviderID: "provider", ModelID: "model", StartedAt: now, FinishedAt: now.Add(time.Second), Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes}
}

func mustCallKey(t *testing.T, call billing.CallUsageRecord) string {
	t.Helper()
	sealed, err := call.Seal()
	if err != nil {
		t.Fatal(err)
	}
	return sealed.Key
}
