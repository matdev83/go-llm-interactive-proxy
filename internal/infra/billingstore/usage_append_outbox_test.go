package billingstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func ensureLegacyUsageAppendOutbox(t *testing.T, store *DurableStore) {
	t.Helper()
	if err := usageAppendOutboxSchemaUp(context.Background(), store.db); err != nil {
		t.Fatalf("create historical outbox fixture: %v", err)
	}
}

func newLegacyOutboxTestStore(t *testing.T) *DurableStore {
	store := newSQLiteTestStore(t)
	ensureLegacyUsageAppendOutbox(t, store)
	return store
}

func TestSQLiteUsageAppendOutboxCutoverDropsOnlyAfterDrain(t *testing.T) {
	t.Parallel()
	store := newLegacyOutboxTestStore(t)
	ctx := context.Background()
	call := testOutboxCall(t)
	if err := store.EnqueueCallUsageAppend(ctx, call, "legacy terminal fallback"); err != nil {
		t.Fatal(err)
	}
	if err := store.CutoverUsageAppendOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetCallUsage(ctx, call.CallID); err != nil {
		t.Fatalf("drained call = %v", err)
	}
	var tables int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name='usage_append_outbox'`).Scan(ctx, &tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("legacy outbox table still exists after cutover")
	}
}

func TestSQLiteUsageAppendOutboxDrainReplaysDeferredRows(t *testing.T) {
	t.Parallel()
	store := newLegacyOutboxTestStore(t)
	ctx := context.Background()
	call := testOutboxCall(t)
	if err := store.EnqueueCallUsageAppend(ctx, call, "deferred"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeferUsageAppend(ctx, mustCallKey(t, call), "temporary outage"); err != nil {
		t.Fatal(err)
	}
	if err := store.DrainUsageAppendOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	if unresolved, err := store.UsageAppendOutboxUnresolved(ctx); err != nil || unresolved != 0 {
		t.Fatalf("unresolved deferred rows = %d, err=%v", unresolved, err)
	}
}

func TestSQLiteUsageAppendOutboxDrainPreservesCurrentRecords(t *testing.T) {
	t.Parallel()
	store := newLegacyOutboxTestStore(t)
	ctx := context.Background()
	call := testOutboxCall(t)
	if err := store.EnqueueCallUsageAppend(ctx, call, "legacy terminal fallback"); err != nil {
		t.Fatal(err)
	}
	if err := store.DrainUsageAppendOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetCallUsage(ctx, call.CallID); err != nil {
		t.Fatalf("drained call = %v", err)
	}
	if unresolved, err := store.UsageAppendOutboxUnresolved(ctx); err != nil || unresolved != 0 {
		t.Fatalf("unresolved = %d, err=%v", unresolved, err)
	}
}

func TestSQLiteUsageAppendOutboxDrainBlocksReplayConflict(t *testing.T) {
	t.Parallel()
	store := newLegacyOutboxTestStore(t)
	ctx := context.Background()
	call := testOutboxCall(t)
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	changed := call
	changed.Outcome = billing.TurnOutcomeFailed
	payload, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	key := mustCallKey(t, changed)
	if _, err := store.db.NewRaw(`INSERT INTO usage_append_outbox(append_key,kind,call_id,payload_json,status,attempt_count,next_attempt_at,last_error,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, key, "call", call.CallID.String(), string(payload), "pending", 0, time.Now().UTC(), "conflicting legacy payload", time.Now().UTC(), time.Now().UTC()).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.DrainUsageAppendOutbox(ctx); !errors.Is(err, ErrUsageAppendDrainBlocked) {
		t.Fatalf("drain = %v, want blocked", err)
	}
	if unresolved, err := store.UsageAppendOutboxUnresolved(ctx); err != nil || unresolved != 1 {
		t.Fatalf("unresolved = %d, err=%v; conflict must remain for reconciliation", unresolved, err)
	}
}

func TestSQLiteUsageAppendOutboxDrainBlocksMalformedRows(t *testing.T) {
	t.Parallel()
	store := newLegacyOutboxTestStore(t)
	ctx := context.Background()
	if _, err := store.db.NewRaw(`INSERT INTO usage_append_outbox(append_key,kind,call_id,payload_json,status,attempt_count,next_attempt_at,last_error,created_at,updated_at) VALUES ('malformed','call','bc_00000000000000000000000000000099','{','pending',0,?,?,?,?)`, time.Now().UTC(), "", time.Now().UTC(), time.Now().UTC()).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.DrainUsageAppendOutbox(ctx); !errors.Is(err, ErrUsageAppendDrainBlocked) {
		t.Fatalf("drain = %v, want blocked", err)
	}
}

func TestSQLiteUsageAppendOutboxListsCallAndLegAndDefersDueWork(t *testing.T) {
	t.Parallel()
	store := newLegacyOutboxTestStore(t)
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
	t.Parallel()
	store := newLegacyOutboxTestStore(t)
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
