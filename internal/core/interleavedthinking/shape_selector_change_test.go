package interleavedthinking

import (
	"context"
	"testing"
)

func TestShapeCall_existingMemoInjectsWhenRouteSelectorChanges(t *testing.T) {
	t.Parallel()
	store, scope, ref := storeWithMemo(t, newMemoState("prior plan"))
	call := baseCall()
	call.Route.Selector = "[thinker]old-thinker:m^old-exec:m"
	res, err := ShapeCall(context.Background(), ShapeInput{
		Call:      call,
		Candidate: executorCandidate(),
		Config:    ShapeConfig{RegularTurnsRemaining: 2},
		MemoStore: store,
		Scope:     scope,
		MemoRef:   &ref,
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if !res.MemoInjected {
		t.Fatalf("selector change must not skip existing A-leg memo: outcome=%q", res.MemoOutcome)
	}
	got, ok, err := store.Get(context.Background(), scope, ref)
	if err != nil || !ok {
		t.Fatalf("Get ok=%v err=%v", ok, err)
	}
	if got.Memo != "prior plan" {
		t.Fatalf("memo mutated on selector change: %q", got.Memo)
	}
	if res.Call.Route.Selector != call.Route.Selector {
		t.Fatalf("shaping must not rewrite selector: got %q", res.Call.Route.Selector)
	}
}
