package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// TestMemoSkipReason_MapsEveryOutcome proves memoSkipReason returns the bounded diagnostic string
// for every MemoOutcome value a shape result can produce, and the empty string for any other
// outcome (injected, expired, the zero value, and unknown values).
func TestMemoSkipReason_MapsEveryOutcome(t *testing.T) {
	t.Parallel()
	cases := []struct {
		outcome interleavedthinking.MemoOutcome
		want    string
	}{
		{interleavedthinking.MemoOutcomeSkippedVisible, "visible"},
		{interleavedthinking.MemoOutcomeSkippedDuplicate, "duplicate"},
		{interleavedthinking.MemoOutcomeSkippedEmpty, "empty"},
		{interleavedthinking.MemoOutcomeSkippedMissing, "missing"},
		{interleavedthinking.MemoOutcomeInjected, ""},
		{interleavedthinking.MemoOutcomeExpired, ""},
		{interleavedthinking.MemoOutcomeNone, ""},
		{"unknown-outcome", ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.outcome), func(t *testing.T) {
			t.Parallel()
			if got := memoSkipReason(tc.outcome); got != tc.want {
				t.Fatalf("memoSkipReason(%q) = %q want %q", tc.outcome, got, tc.want)
			}
		})
	}
}

// TestInterleavedPhaseForRole_MapsEveryRole covers interleavedPhaseForRole for every Role value
// so a new role added without updating the switch surfaces as an empty phase rather than a silent
// mislabeling.
func TestInterleavedPhaseForRole_MapsEveryRole(t *testing.T) {
	t.Parallel()
	cases := []struct {
		role interleavedstate.Role
		want string
	}{
		{interleavedstate.RoleThinker, "thinker"},
		{interleavedstate.RoleExecutor, "executor"},
		{interleavedstate.RoleNone, ""},
		{"unknown-role", ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.role), func(t *testing.T) {
			t.Parallel()
			if got := interleavedPhaseForRole(tc.role); got != tc.want {
				t.Fatalf("interleavedPhaseForRole(%q) = %q want %q", tc.role, got, tc.want)
			}
		})
	}
}

type recBLeg struct {
	callsLog []string
}

func (r *recBLeg) Recv(context.Context) (lipapi.Event, error) {
	return lipapi.Event{}, nil
}

func (r *recBLeg) Cancel(_ context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	r.callsLog = append(r.callsLog, "cancel:"+string(cause.Kind))
	return lipapi.CancelResult{Mode: lipapi.CancelModeProvider}
}

func (r *recBLeg) Close() error {
	r.callsLog = append(r.callsLog, "close")
	return nil
}

func (r *recBLeg) calls() []string {
	return r.callsLog
}

func TestPersistCapturedMemo_ReplacesMemoAndDeletesPrevious(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	aLeg, err := st.CreateALeg(ctx, "memo-replace")
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)
	ex := TestExecutor()
	ex.Store = st
	ex.MemoStore = memoStore
	ex.InterleavedConfig = interleavedthinking.ShapeConfig{Instructions: "think"}
	scope := interleavedthinking.Scope(aLeg.ALegID)
	state := interleavedstate.State{}

	state, err = ex.persistCapturedMemo(ctx, aLeg.ALegID, state, interleavedthinking.MemoState{Memo: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if state.MemoRef == nil {
		t.Fatal("expected memo ref after first capture")
	}
	firstRef := *state.MemoRef
	if _, ok, err := memoStore.Get(ctx, scope, firstRef); err != nil || !ok {
		t.Fatalf("first memo must exist: ok=%v err=%v", ok, err)
	}

	state, err = ex.persistCapturedMemo(ctx, aLeg.ALegID, state, interleavedthinking.MemoState{Memo: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if state.MemoRef == nil || state.MemoRef.Equal(firstRef) {
		t.Fatalf("expected new memo ref, got %+v (was %+v)", state.MemoRef, firstRef)
	}
	got, ok, err := memoStore.Get(ctx, scope, *state.MemoRef)
	if err != nil || !ok || got.Memo != "second" {
		t.Fatalf("new memo: ok=%v err=%v memo=%q", ok, err, got.Memo)
	}
	if _, ok, err := memoStore.Get(ctx, scope, firstRef); err != nil {
		t.Fatalf("lookup old memo: %v", err)
	} else if ok {
		t.Fatalf("previous memo ref %v must be deleted from store", firstRef)
	}
	persisted, err := st.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.MemoRef == nil || !persisted.MemoRef.Equal(*state.MemoRef) {
		t.Fatalf("persisted memo ref = %+v want %+v", persisted.MemoRef, state.MemoRef)
	}
}

func TestPersistCapturedMemo_RollbackOnPersistFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	st := &failInterleavedPersistStore{MemoryStore: base}
	aLeg, err := st.CreateALeg(ctx, "memo-persist-fail")
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)
	ex := TestExecutor()
	ex.Store = st
	ex.MemoStore = memoStore
	ex.InterleavedConfig = interleavedthinking.ShapeConfig{Instructions: "think"}
	scope := interleavedthinking.Scope(aLeg.ALegID)
	oldRef, err := memoStore.Put(ctx, scope, interleavedthinking.MemoState{Memo: "keep-me"})
	if err != nil {
		t.Fatal(err)
	}
	state := interleavedstate.State{MemoRef: &oldRef}

	resultState, err := ex.persistCapturedMemo(ctx, aLeg.ALegID, state, interleavedthinking.MemoState{Memo: "orphan"})
	if !errors.Is(err, errInjectedCyclePersist) {
		t.Fatalf("want errInjectedCyclePersist, got %v", err)
	}
	if resultState.MemoRef == nil || !resultState.MemoRef.Equal(oldRef) {
		t.Fatalf("returned memo ref must restore old ref, got %+v want %+v", resultState.MemoRef, oldRef)
	}
	if _, ok, err := memoStore.Get(ctx, scope, oldRef); err != nil || !ok {
		t.Fatalf("original memo must remain: ok=%v err=%v", ok, err)
	}
	orphanRef := interleavedstate.MemoRef{Key: "memo-2", Version: 1}
	if _, ok, err := memoStore.Get(ctx, scope, orphanRef); err == nil && ok {
		t.Fatal("orphan memo must be deleted when interleaved state persist fails")
	}
}

func TestInterleavedContinuationStream_UnknownPhaseRecvError(t *testing.T) {
	t.Parallel()
	s := &interleavedContinuationStream{}
	_, err := s.Recv(context.Background())
	if !errors.Is(err, errUnknownInterleavedPhase) {
		t.Fatalf("got %v", err)
	}
}
