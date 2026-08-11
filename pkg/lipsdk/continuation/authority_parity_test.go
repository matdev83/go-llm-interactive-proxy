package continuation

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// TestAuthorityParity exercises the SDK authority's complete mutable-state and
// recorder lifecycle surface. Core now delegates to this implementation.
func TestAuthorityParity(t *testing.T) {
	ctx := context.Background()
	scope := Scope{TenantID: "tenant", PrincipalID: "principal", SessionID: "session"}
	store := NewMemoryStoreWithLimits(StorageLimits{MaxRecords: 4, MaxRecordBytes: 4096, MaxBytes: 1 << 20, MaxChainDepth: 4})
	now := time.Unix(1700000000, 0)
	store.SetClock(func() time.Time { return now })
	policy := StoragePolicy{Mode: PersistencePersistent, TTL: time.Hour, AllowIncomplete: true}
	id, err := store.Reserve(ctx, scope, policy)
	if err != nil {
		t.Fatal(err)
	}
	record := ContinuationRecord{ID: id, Scope: scope, Policy: policy, Terminal: true, Status: RecordStatusCompleted, InputItems: []lipapi.Item{{Kind: lipapi.ItemKindMessage}}}
	if err := store.PutTerminal(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := Lookup(ctx, store, Scope{TenantID: "other", PrincipalID: "principal", SessionID: "session"}, id); err != ErrPreviousResponseNotFound {
		t.Fatalf("scope isolation error=%v", err)
	}
	got, err := Lookup(ctx, store, scope, id)
	if err != nil || got.ID != id {
		t.Fatalf("lookup=%+v err=%v", got, err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := Lookup(ctx, store, scope, id); err != ErrPreviousResponseNotFound {
		t.Fatalf("expiry error=%v", err)
	}

	id, err = store.Reserve(ctx, scope, policy)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := 0
	recorder := NewStreamRecorder(TerminalRecorder{Store: store}, ContinuationRecord{ID: id, Scope: scope, Policy: policy, InputItems: []lipapi.Item{{Kind: lipapi.ItemKindMessage}}}, func() { cleanup++ })
	recorder.Observe(ctx, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "ok"})
	recorder.Observe(ctx, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"})
	recorder.Observe(ctx, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"})
	if cleanup != 0 || !recorder.ContinuationReservationCleanupConsumed() {
		t.Fatalf("terminal lifecycle cleanup=%d consumed=%v", cleanup, recorder.ContinuationReservationCleanupConsumed())
	}
	if _, err := Lookup(ctx, store, scope, id); err != nil {
		t.Fatal(err)
	}

	id, err = store.Reserve(ctx, scope, policy)
	if err != nil {
		t.Fatal(err)
	}
	incomplete := NewStreamRecorder(TerminalRecorder{Store: store}, ContinuationRecord{ID: id, Scope: scope, Policy: policy}, func() { cleanup++ })
	incomplete.Observe(ctx, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "partial"})
	if err := incomplete.FinalizeIncomplete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := incomplete.Close(); err != nil || cleanup != 0 {
		t.Fatalf("incomplete lifecycle err=%v cleanup=%d", err, cleanup)
	}
	if err := store.Close(); err != nil || store.Close() != nil {
		t.Fatalf("close lifecycle err=%v", err)
	}
}
