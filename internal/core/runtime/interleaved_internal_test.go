package runtime

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// TestReleaseBLegs_NilScopeAndLegsAreNoOps covers the nil-input guards of releaseBLegs.
// It must not panic on nil scope, nil legs, or an empty legs slice, and it must not touch scopes
// when there is nothing to release.
func TestReleaseBLegs_NilScopeAndLegsAreNoOps(t *testing.T) {
	t.Parallel()
	t.Run("nil_scope", func(t *testing.T) {
		t.Parallel()
		releaseBLegs(nil, []*parallelLeg{{bleg: b2bua.BLegRecord{BLegID: "b1"}}})
	})
	t.Run("nil_legs", func(t *testing.T) {
		t.Parallel()
		c := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
		a := c.StartALeg("a-no-legs")
		releaseBLegs(a, nil)
		releaseBLegs(a, []*parallelLeg{})
	})
}

// TestReleaseBLegs_ReleasesEveryLegFromScope proves releaseBLegs removes every leg's B-leg id
// from the scope so a subsequent CancelALeg does not cancel or close them.
func TestReleaseBLegs_ReleasesEveryLegFromScope(t *testing.T) {
	t.Parallel()
	c := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: 0})
	a := c.StartALeg("a-release-legs")
	survivor := &recBLeg{}
	released := &recBLeg{}
	if err := a.RegisterBLeg(t.Context(), leglifecycle.BLegHandle{ID: "kept", Attempt: survivor}); err != nil {
		t.Fatal(err)
	}
	if err := a.RegisterBLeg(t.Context(), leglifecycle.BLegHandle{ID: "released", Attempt: released}); err != nil {
		t.Fatal(err)
	}

	releaseBLegs(a, []*parallelLeg{
		{bleg: b2bua.BLegRecord{BLegID: "released"}},
	})

	if got := released.calls(); got != nil {
		t.Fatalf("released leg touched before CancelALeg: %v", got)
	}
	if err := c.CancelALeg(t.Context(), "a-release-legs", leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatal(err)
	}
	if got := released.calls(); got != nil {
		t.Fatalf("released leg must not be canceled by CancelALeg after releaseBLegs, got %v", got)
	}
	if got, want := survivor.calls(), []string{"cancel:explicit", "close"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("survivor must still be canceled and closed: got %v want %v", got, want)
	}
}

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
	ex := &Executor{
		Store:             st,
		MemoStore:         memoStore,
		InterleavedConfig: interleavedthinking.ShapeConfig{Instructions: "think"},
	}
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
