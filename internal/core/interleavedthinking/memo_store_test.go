package interleavedthinking

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
)

func newMemoState(body string) MemoState {
	return MemoState{
		Memo:                  body,
		SourceSelector:        "openai-responses:gpt-4o[thinker]",
		Backend:               "openai-responses",
		Model:                 "gpt-4o",
		RequestID:             "req-1",
		RegularTurnsRemaining: 2,
		ExtractionSource:      "block",
	}
}

func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestMemoStore_PutGetRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	state := newMemoState("plan A")

	ref, err := store.Put(ctx, "session-1", state)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if ref.Key == "" || ref.Version == 0 {
		t.Fatalf("put returned invalid ref %+v", ref)
	}
	got, ok, err := store.Get(ctx, "session-1", ref)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatal("expected memo to be found")
	}
	if got.Memo != "plan A" || got.SourceSelector != state.SourceSelector {
		t.Fatalf("get returned wrong state: %+v", got)
	}
	if got.InjectedCount != 0 {
		t.Fatalf("expected injected count 0, got %d", got.InjectedCount)
	}
}

func TestMemoStore_GetMissing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	if _, ok, err := store.Get(ctx, "session-1", interleavedstate.MemoRef{Key: "nope"}); err != nil {
		t.Fatalf("get missing: %v", err)
	} else if ok {
		t.Fatal("expected not found for missing key")
	}
}

func TestMemoStore_Update(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	ref, err := store.Put(ctx, "session-1", newMemoState("plan A"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	updated := newMemoState("plan B")
	gotRef, err := store.Update(ctx, "session-1", ref, updated)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if gotRef.Key != ref.Key {
		t.Fatalf("update returned wrong key: %q want %q", gotRef.Key, ref.Key)
	}
	if gotRef.Version != ref.Version+1 {
		t.Fatalf("update must bump version: got %d want %d", gotRef.Version, ref.Version+1)
	}
	got, ok, err := store.Get(ctx, "session-1", ref)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok || got.Memo != "plan B" {
		t.Fatalf("update did not persist: %+v", got)
	}
}

func TestMemoStore_UpdateMissingFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	if _, err := store.Update(ctx, "session-1", interleavedstate.MemoRef{Key: "nope"}, newMemoState("x")); !errors.Is(err, ErrMemoNotFound) {
		t.Fatalf("expected ErrMemoNotFound, got %v", err)
	}
}

func TestMemoStore_Delete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	ref, err := store.Put(ctx, "session-1", newMemoState("plan A"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := store.Delete(ctx, "session-1", ref); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, err := store.Get(ctx, "session-1", ref); err != nil {
		t.Fatalf("get after delete: %v", err)
	} else if ok {
		t.Fatal("expected memo to be gone after delete")
	}
}

func TestMemoStore_Update_ModifyStateFields(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	ref, err := store.Put(ctx, "session-1", newMemoState("plan A"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := store.Get(ctx, "session-1", ref)
	if err != nil || !ok {
		t.Fatalf("get: %v ok=%v", err, ok)
	}
	got.RegularTurnsRemaining--
	if got.RegularTurnsRemaining < 0 {
		got.RegularTurnsRemaining = 0
	}
	got.InjectedCount++
	bumpedRef, err := store.Update(ctx, "session-1", ref, got)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if bumpedRef.Version != ref.Version+1 {
		t.Fatalf("expected bumped version %d, got %d", ref.Version+1, bumpedRef.Version)
	}
	updated, ok, err := store.Get(ctx, "session-1", bumpedRef)
	if err != nil || !ok {
		t.Fatalf("get updated: %v ok=%v", err, ok)
	}
	if updated.RegularTurnsRemaining != 1 {
		t.Fatalf("expected remaining 1, got %d", updated.RegularTurnsRemaining)
	}
	if updated.InjectedCount != 1 {
		t.Fatalf("expected injected count 1, got %d", updated.InjectedCount)
	}
}

func TestMemoStore_ScopeIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	ref, err := store.Put(ctx, "session-A", newMemoState("plan A"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, ok, err := store.Get(ctx, "session-B", ref); err != nil {
		t.Fatalf("get from unrelated scope: %v", err)
	} else if ok {
		t.Fatal("memo must not be visible from an unrelated scope")
	}
	if _, err := store.Update(ctx, "session-B", ref, newMemoState("hijack")); !errors.Is(err, ErrMemoNotFound) {
		t.Fatalf("cross-scope update must be denied, got %v", err)
	}
	if err := store.Delete(ctx, "session-B", ref); err != nil {
		t.Fatalf("cross-scope delete should be idempotent no-op, got %v", err)
	}
	if _, ok, err := store.Get(ctx, "session-A", ref); err != nil || !ok {
		t.Fatalf("original scope memo must still be present after unrelated delete: err=%v ok=%v", err, ok)
	}
}

func TestMemoStore_SizeLimit_RejectsOversized(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(8)
	big := newMemoState(strings.Repeat("x", 32))
	if _, err := store.Put(ctx, "session-1", big); !errors.Is(err, ErrMemoTooLarge) {
		t.Fatalf("put oversized must return ErrMemoTooLarge, got %v", err)
	}
	ref, err := store.Put(ctx, "session-1", newMemoState("small"))
	if err != nil {
		t.Fatalf("put small: %v", err)
	}
	if _, err := store.Update(ctx, "session-1", ref, newMemoState(strings.Repeat("y", 32))); !errors.Is(err, ErrMemoTooLarge) {
		t.Fatalf("update oversized must return ErrMemoTooLarge, got %v", err)
	}
}

func TestMemoStore_SizeLimit_ZeroDisables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(0)
	if _, err := store.Put(ctx, "session-1", newMemoState(strings.Repeat("x", 1<<16))); err != nil {
		t.Fatalf("unbounded store must accept large memo, got %v", err)
	}
}

func TestMemoStore_PutBumpsVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	ref, err := store.Put(ctx, "session-1", newMemoState("a"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if ref.Version != 1 {
		t.Fatalf("expected initial version 1, got %d", ref.Version)
	}
	updatedRef, err := store.Update(ctx, "session-1", ref, newMemoState("b"))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updatedRef.Version != ref.Version+1 {
		t.Fatalf("expected bumped version %d, got %d", ref.Version+1, updatedRef.Version)
	}
}

func TestMemoStore_Put_CanceledContext(t *testing.T) {
	t.Parallel()
	store := NewMemoStore(4096)
	if _, err := store.Put(canceledCtx(), "session-1", newMemoState("a")); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestMemoStore_Get_CanceledContext(t *testing.T) {
	t.Parallel()
	store := NewMemoStore(4096)
	ref, err := store.Put(context.Background(), "session-1", newMemoState("a"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, _, err := store.Get(canceledCtx(), "session-1", ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestMemoStore_Update_CanceledContext(t *testing.T) {
	t.Parallel()
	store := NewMemoStore(4096)
	ref, err := store.Put(context.Background(), "session-1", newMemoState("a"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := store.Update(canceledCtx(), "session-1", ref, newMemoState("b")); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestMemoStore_Delete_CanceledContext(t *testing.T) {
	t.Parallel()
	store := NewMemoStore(4096)
	ref, err := store.Put(context.Background(), "session-1", newMemoState("a"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := store.Delete(canceledCtx(), "session-1", ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestMemoStore_CanceledContextDoesNotMutate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	ref, err := store.Put(ctx, "session-1", newMemoState("a"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := store.Update(canceledCtx(), "session-1", ref, newMemoState("b")); !errors.Is(err, context.Canceled) {
		t.Fatalf("update: %v", err)
	}
	got, ok, err := store.Get(ctx, "session-1", ref)
	if err != nil || !ok {
		t.Fatalf("get: %v ok=%v", err, ok)
	}
	if got.Memo != "a" {
		t.Fatalf("canceled update must not mutate state, got %q", got.Memo)
	}
}

func TestMemoStore_EmptyScopeRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	ref := interleavedstate.MemoRef{Key: "memo-1"}

	if _, err := store.Put(ctx, "", newMemoState("a")); !errors.Is(err, ErrEmptyScope) {
		t.Fatalf("put empty scope: want ErrEmptyScope, got %v", err)
	}
	if _, _, err := store.Get(ctx, "", ref); !errors.Is(err, ErrEmptyScope) {
		t.Fatalf("get empty scope: want ErrEmptyScope, got %v", err)
	}
	if _, err := store.Update(ctx, "", ref, newMemoState("a")); !errors.Is(err, ErrEmptyScope) {
		t.Fatalf("update empty scope: want ErrEmptyScope, got %v", err)
	}
	if err := store.Delete(ctx, "", ref); !errors.Is(err, ErrEmptyScope) {
		t.Fatalf("delete empty scope: want ErrEmptyScope, got %v", err)
	}
}

func TestMemoStore_EmptyMemoRefRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	emptyRef := interleavedstate.MemoRef{}

	if _, _, err := store.Get(ctx, "session-1", emptyRef); !errors.Is(err, ErrEmptyMemoRef) {
		t.Fatalf("get empty ref: want ErrEmptyMemoRef, got %v", err)
	}
	if _, err := store.Update(ctx, "session-1", emptyRef, newMemoState("a")); !errors.Is(err, ErrEmptyMemoRef) {
		t.Fatalf("update empty ref: want ErrEmptyMemoRef, got %v", err)
	}
	if err := store.Delete(ctx, "session-1", emptyRef); !errors.Is(err, ErrEmptyMemoRef) {
		t.Fatalf("delete empty ref: want ErrEmptyMemoRef, got %v", err)
	}
}
