package continuation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	corecont "github.com/matdev83/go-llm-interactive-proxy/internal/core/continuation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

type phase42Recorder struct {
	calls  int
	err    error
	record lipcont.ContinuationRecord
}

type phase42PanicRecorder struct{}

func (*phase42PanicRecorder) RecordTerminal(context.Context, lipcont.ContinuationRecord) error {
	panic("recorder panic")
}

type phase42TrackingStore struct {
	*corecont.MemoryStore
	deletes int
}

func (s *phase42TrackingStore) Delete(ctx context.Context, scope lipcont.Scope, id lipcont.ResponseID) error {
	s.deletes++
	return s.MemoryStore.Delete(ctx, scope, id)
}

func (r *phase42Recorder) RecordTerminal(_ context.Context, record lipcont.ContinuationRecord) error {
	r.calls++
	r.record = lipcont.CloneRecord(record)
	return r.err
}

func TestStreamRecorderStoresOnlyTerminalAndCloseIsIdempotent(t *testing.T) {
	backend := &phase42Recorder{}
	r := corecont.NewStreamRecorder(backend, lipcont.ContinuationRecord{InputItems: []lipapi.Item{phase42Item("input", lipapi.RoleUser)}}, func() {})
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"})
	if backend.calls != 0 {
		t.Fatal("stored before terminal")
	}
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
	if backend.calls != 1 || len(backend.record.OutputItems) != 1 || backend.record.OutputItems[0].Content[0].Text != "hello" {
		t.Fatalf("record=%+v calls=%d", backend.record, backend.calls)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamRecorderCloseReleasesUnfinishedReservation(t *testing.T) {
	cleanupCalls := 0
	recorder := corecont.NewStreamRecorder(&phase42Recorder{}, lipcont.ContinuationRecord{}, func() {
		cleanupCalls++
	})
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if cleanupCalls != 1 {
		t.Fatalf("cleanup calls=%d, want exactly 1", cleanupCalls)
	}
}

func TestStreamRecorderStorageFailureDoesNotBecomeStreamFailure(t *testing.T) {
	backend := &phase42Recorder{err: errors.New("storage down")}
	cleanupCalls := 0
	r := corecont.NewStreamRecorder(backend, lipcont.ContinuationRecord{}, func() {
		cleanupCalls++
	})
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
	if !errors.Is(r.StorageError(), backend.err) || backend.calls != 1 {
		t.Fatalf("storage error=%v calls=%d", r.StorageError(), backend.calls)
	}
	if cleanupCalls != 1 {
		t.Fatalf("generic terminal persistence failure cleanup calls=%d, want 1", cleanupCalls)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if cleanupCalls != 1 {
		t.Fatalf("generic terminal persistence failure cleanup calls after Close=%d, want 1", cleanupCalls)
	}
}

func TestStreamRecorderPanickingCleanupDoesNotPropagateOrRepeat(t *testing.T) {
	cleanupCalls := 0
	r := corecont.NewStreamRecorder(&phase42Recorder{err: errors.New("storage down")}, lipcont.ContinuationRecord{}, func() {
		cleanupCalls++
		panic("cleanup panic")
	})
	// A cleanup panic must not escape Observe, and the detached callback must
	// not be invoked again by Close.
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
	if cleanupCalls != 1 {
		t.Fatalf("panicking cleanup calls=%d, want 1", cleanupCalls)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if cleanupCalls != 1 {
		t.Fatalf("panicking cleanup calls after Close=%d, want 1", cleanupCalls)
	}
}

func TestStreamRecorderObservePanicReleasesExactlyOnce(t *testing.T) {
	cleanupCalls := 0
	r := corecont.NewStreamRecorder(&phase42PanicRecorder{}, lipcont.ContinuationRecord{}, func() {
		cleanupCalls++
	})
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
	if cleanupCalls != 1 {
		t.Fatalf("cleanup calls=%d, want exactly 1 after Observe panic", cleanupCalls)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if cleanupCalls != 1 {
		t.Fatalf("cleanup calls=%d, want exactly 1 after Close", cleanupCalls)
	}
}

func TestStreamRecorderFinalizeIncompletePanicReleasesExactlyOnce(t *testing.T) {
	cleanupCalls := 0
	r := corecont.NewStreamRecorder(&phase42PanicRecorder{}, lipcont.ContinuationRecord{}, func() {
		cleanupCalls++
	})
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "partial"})
	if err := r.FinalizeIncomplete(context.Background()); err == nil {
		t.Fatal("FinalizeIncomplete unexpectedly succeeded after recorder panic")
	}
	if cleanupCalls != 1 {
		t.Fatalf("cleanup calls=%d, want exactly 1 after panic", cleanupCalls)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if cleanupCalls != 1 {
		t.Fatalf("cleanup calls=%d, want exactly 1 after Close", cleanupCalls)
	}
}

func TestStreamRecorderFinalizeIncompleteFailureReleasesExactlyOnce(t *testing.T) {
	backend := &phase42Recorder{err: errors.New("storage down")}
	cleanupCalls := 0
	r := corecont.NewStreamRecorder(backend, lipcont.ContinuationRecord{}, func() {
		cleanupCalls++
	})
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "partial"})
	if err := r.FinalizeIncomplete(context.Background()); !errors.Is(err, backend.err) {
		t.Fatalf("FinalizeIncomplete error=%v, want %v", err, backend.err)
	}
	if cleanupCalls != 1 {
		t.Fatalf("cleanup calls=%d, want exactly 1 after failed finalization", cleanupCalls)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if cleanupCalls != 1 {
		t.Fatalf("cleanup calls=%d, want exactly 1 after Close", cleanupCalls)
	}
}

func TestStreamRecorderOverflowReleasesReservedRecordIndependentlyOfRecorder(t *testing.T) {
	store := &phase42TrackingStore{MemoryStore: corecont.NewMemoryStoreWithLimits(lipcont.StorageLimits{MaxRecords: 1})}
	scope := lipcont.Scope{PrincipalID: "overflow-principal", SessionID: "overflow-session"}
	policy := lipcont.StoragePolicy{
		Mode: lipcont.PersistencePersistent,
		TTL:  time.Hour,
		Limits: lipcont.StorageLimits{
			MaxRecordBytes: 4,
		},
	}
	id, err := store.Reserve(context.Background(), scope, policy)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}

	backend := &phase42Recorder{}
	cleanupCalls := 0
	recorder := corecont.NewStreamRecorder(backend, lipcont.ContinuationRecord{
		ID:     id,
		Scope:  scope,
		Policy: policy,
	}, func() {
		cleanupCalls++
		_ = store.Delete(context.Background(), scope, id)
	})
	recorder.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "too large"})

	if cleanupCalls != 1 {
		t.Fatalf("cleanup calls=%d, want 1", cleanupCalls)
	}
	if store.deletes != 1 {
		t.Fatalf("store deletes=%d, want exactly 1", store.deletes)
	}
	if _, err := store.Reserve(context.Background(), scope, policy); err != nil {
		t.Fatalf("overflow left reservation occupied: %v", err)
	}
}

func TestStreamRecorderPreservesTextReasoningTextOrder(t *testing.T) {
	backend := &phase42Recorder{}
	r := corecont.NewStreamRecorder(backend, lipcont.ContinuationRecord{}, func() {})
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "before"})
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: "thought"})
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "after"})
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
	if backend.calls != 1 || len(backend.record.OutputItems) != 3 {
		t.Fatalf("output=%+v calls=%d", backend.record.OutputItems, backend.calls)
	}
	if backend.record.OutputItems[0].Content[0].Text != "before" || backend.record.OutputItems[1].Reasoning == nil || backend.record.OutputItems[1].Reasoning.Reasoning.Text != "thought" || backend.record.OutputItems[2].Content[0].Text != "after" {
		t.Fatalf("output order=%+v", backend.record.OutputItems)
	}
}

func TestStreamRecorderPreservesToolCallItems(t *testing.T) {
	backend := &phase42Recorder{}
	r := corecont.NewStreamRecorder(backend, lipcont.ContinuationRecord{}, func() {})
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "text before tool"})
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_123", ToolName: "get_weather"})
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_123", Delta: `{"city":`})
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_123", Delta: `"NYC"}`})
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: "call_123"})
	r.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})

	if backend.calls != 1 {
		t.Fatalf("expected 1 record call, got %d", backend.calls)
	}
	if len(backend.record.OutputItems) != 2 {
		t.Fatalf("expected 2 output items (text + tool call), got %d: %+v", len(backend.record.OutputItems), backend.record.OutputItems)
	}
	if backend.record.OutputItems[0].Kind != lipapi.ItemKindMessage || backend.record.OutputItems[0].Content[0].Text != "text before tool" {
		t.Fatalf("item[0] mismatch: %+v", backend.record.OutputItems[0])
	}
	tcItem := backend.record.OutputItems[1]
	if tcItem.Kind != lipapi.ItemKindToolCall || tcItem.ToolCall == nil {
		t.Fatalf("item[1] should be tool call, got: %+v", tcItem)
	}
	if tcItem.ToolCall.CallID != "call_123" || tcItem.ToolCall.Name != "get_weather" || string(tcItem.ToolCall.Arguments) != `{"city":"NYC"}` {
		t.Fatalf("tool call fields mismatch: %+v", tcItem.ToolCall)
	}
}
