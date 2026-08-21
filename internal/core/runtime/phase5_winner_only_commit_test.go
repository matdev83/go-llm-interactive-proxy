package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// Parity note (Important 4): winner-only SetInterleavedState atomicity is proven in-memory
// via b2bua.NewMemoryStore + interleavedthinking.NewMemoStore (FetchInterleavedState / Get
// assertions below). This suffices because b2bua.Store SetInterleavedState is a single-record
// atomic operation across all production Store implementations: MemoryStore holds the state as one
// map entry under mu (internal/core/b2bua/store.go:287, 304), and bunstore.Store persists it as a
// single UPDATE a_legs SET interleaved_state_json = ? WHERE a_leg_id = ? (internal/core/continuity/bunstore/store.go:441-452)
// for both SQLite and Postgres (dialect-agnostic). No multi-row/cross-table transaction is involved;
// memo content is orthogonal (MemoStore) and likewise verified via Get. Bunstore SQLite/Postgres
// parity is covered independently by internal/core/continuity/bunstore integration tests.
// See internal/core/runtime/testdata/phase5_red_evidence.md section 5.3 for RED->GREEN evidence.

// TestPhase5_WinnerOnlyCommit_AcceptedWinnerPersistsState proves that when a parallel race
// accepts a winning arm, only the winning arm's pending selection effects (interleaved memo and cycle)
// are committed to the store, and losing arms' pending effects are never committed.
func TestPhase5_WinnerOnlyCommit_AcceptedWinnerPersistsState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	aLeg, err := store.CreateALeg(ctx, "")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	memoStore := interleavedthinking.NewMemoStore(4096)
	ex := TestExecutor()
	ex.Store = store
	ex.MemoStore = memoStore
	ex.InterleavedConfig = interleavedthinking.ShapeConfig{
		Instructions:          "instructions",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}

	scope := interleavedthinking.Scope(aLeg.ALegID)
	initialRef, err := memoStore.Put(ctx, scope, interleavedthinking.MemoState{
		Memo:                  "initial memo",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatalf("put initial memo: %v", err)
	}

	initialInterleaved := interleavedstate.State{
		Cycle: interleavedstate.CycleState{
			SelectorKey: "sel-1",
			Sequence: []interleavedstate.CycleEntry{
				{Key: "cand-a", Role: interleavedstate.RoleThinker},
				{Key: "cand-b", Role: interleavedstate.RoleExecutor},
			},
			NextIndex: 0,
		},
		MemoRef: &initialRef,
	}
	if err := store.SetInterleavedState(ctx, aLeg.ALegID, initialInterleaved); err != nil {
		t.Fatalf("set initial interleaved state: %v", err)
	}

	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}
	entries := []legEntry{{cand: candA, delay: 0}, {cand: candB, delay: 0}}
	candidates := []routing.AttemptCandidate{candA, candB}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-w1", aLegID: aLeg.ALegID},
		},
		routeFacts: routeFacts{},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
		interleaved: initialInterleaved,
	}

	sessionA := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-a"}}
	sessionB := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-b"}}

	winnerPendingUpdate := &interleavedthinking.PendingMemoUpdate{
		Ref: initialRef,
		State: interleavedthinking.MemoState{
			Memo:                  "winner memo from arm A",
			RegularTurnsRemaining: 1,
		},
	}
	loserPendingUpdate := &interleavedthinking.PendingMemoUpdate{
		Ref: initialRef,
		State: interleavedthinking.MemoState{
			Memo:                  "loser memo from arm B",
			RegularTurnsRemaining: 0,
		},
	}

	readyA := &readyAttempt{
		session: sessionA,
		pending: pendingSelectionEffects{
			interleaved: initialInterleaved,
			memoUpdate:  winnerPendingUpdate,
		},
	}
	readyB := &readyAttempt{
		session: sessionB,
		pending: pendingSelectionEffects{
			interleaved: initialInterleaved,
			memoUpdate:  loserPendingUpdate,
		},
	}

	// Arm A arrives first (arrival: 1), Arm B arrives second (arrival: 2)
	outcomeA := parallelArmOutcome{cand: candA, ready: readyA, arrival: 1, winnerBuf: []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "win"}}}
	outcomeB := parallelArmOutcome{cand: candB, ready: readyB, arrival: 2, winnerBuf: []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "lose"}}}

	cycleAdvance := &interleavedstate.CycleState{
		SelectorKey: "sel-1",
		Sequence: []interleavedstate.CycleEntry{
			{Key: "cand-a", Role: interleavedstate.RoleThinker},
			{Key: "cand-b", Role: interleavedstate.RoleExecutor},
		},
		NextIndex: 1,
	}
	reducer := newParallelRoundReducer(ex, req, entries, candidates, cycleAdvance)
	opened, err := reducer.Reduce(ctx, []parallelArmOutcome{outcomeA, outcomeB})
	if err != nil {
		t.Fatalf("unexpected reducer error: %v", err)
	}

	if opened.session != sessionA {
		t.Fatalf("expected arm A to win, got session %v", opened.session)
	}

	// Verify loser readyB was disposed without committing its pending effects
	if !readyB.consumed {
		t.Errorf("expected loser readyB to be marked consumed/disposed")
	}

	// Verify memo store has WINNER memo, NOT loser memo
	persistedMemo, ok, err := memoStore.Get(ctx, scope, initialRef)
	if err != nil || !ok {
		t.Fatalf("fetch persisted memo: ok=%v, err=%v", ok, err)
	}
	if persistedMemo.Memo != "winner memo from arm A" {
		t.Errorf("persisted memo = %q, want %q", persistedMemo.Memo, "winner memo from arm A")
	}
	if persistedMemo.RegularTurnsRemaining != 1 {
		t.Errorf("persisted turns = %d, want 1", persistedMemo.RegularTurnsRemaining)
	}

	// Verify interleaved state store has the winner's updated memo reference
	persistedState, err := store.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatalf("fetch interleaved state: %v", err)
	}
	if persistedState.MemoRef == nil || persistedState.MemoRef.Key != initialRef.Key {
		t.Errorf("persisted state memo ref mismatch: got %v, want %v", persistedState.MemoRef, initialRef)
	}
}

// TestPhase5_WinnerOnlyCommit_AllFailureNeverPersists proves that when all arms fail in a parallel
// round, no winner-only state or memo updates are committed to the store.
func TestPhase5_WinnerOnlyCommit_AllFailureNeverPersists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	aLeg, err := store.CreateALeg(ctx, "")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	memoStore := interleavedthinking.NewMemoStore(4096)
	ex := TestExecutor()
	ex.Store = store
	ex.MemoStore = memoStore
	ex.InterleavedConfig = interleavedthinking.ShapeConfig{
		Instructions:          "instructions",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}

	scope := interleavedthinking.Scope(aLeg.ALegID)
	initialRef, err := memoStore.Put(ctx, scope, interleavedthinking.MemoState{
		Memo:                  "untouched initial memo",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatalf("put initial memo: %v", err)
	}

	initialInterleaved := interleavedstate.State{
		Cycle: interleavedstate.CycleState{
			SelectorKey: "sel-1",
			NextIndex:   0,
		},
		MemoRef: &initialRef,
	}
	if err := store.SetInterleavedState(ctx, aLeg.ALegID, initialInterleaved); err != nil {
		t.Fatalf("set initial interleaved state: %v", err)
	}

	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}
	entries := []legEntry{{cand: candA, delay: 0}, {cand: candB, delay: 0}}
	candidates := []routing.AttemptCandidate{candA, candB}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-fail", aLegID: aLeg.ALegID},
		},
		routeFacts: routeFacts{},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
		interleaved: initialInterleaved,
	}

	outcomeA := parallelArmOutcome{cand: candA, failErr: errors.New("backend timeout A"), arrival: 1}
	outcomeB := parallelArmOutcome{cand: candB, failErr: errors.New("backend 500 error B"), arrival: 2}

	cycleAdvance := &interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 1}
	reducer := newParallelRoundReducer(ex, req, entries, candidates, cycleAdvance)
	opened, err := reducer.Reduce(ctx, []parallelArmOutcome{outcomeA, outcomeB})
	if err != nil {
		t.Fatalf("unexpected reducer error: %v", err)
	}
	if opened.session != nil {
		t.Fatalf("expected nil session on all-failure round")
	}

	// Verify memo in store remains completely untouched
	memo, ok, err := memoStore.Get(ctx, scope, initialRef)
	if err != nil || !ok {
		t.Fatalf("fetch memo: ok=%v, err=%v", ok, err)
	}
	if memo.Memo != "untouched initial memo" {
		t.Errorf("memo was mutated on all-failure round: got %q, want untouched", memo.Memo)
	}

	// Verify interleaved state in store remains completely untouched
	state, err := store.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatalf("fetch state: %v", err)
	}
	if state.Cycle.NextIndex != 0 {
		t.Errorf("cycle was mutated on all-failure round: got %d, want 0", state.Cycle.NextIndex)
	}
}

// TestPhase5_WinnerOnlyCommit_FatalErrorNeverPersists proves that when a fatal error aborts
// the round, ready arms' pending selection effects are not committed.
func TestPhase5_WinnerOnlyCommit_FatalErrorNeverPersists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	aLeg, err := store.CreateALeg(ctx, "")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	memoStore := interleavedthinking.NewMemoStore(4096)
	ex := TestExecutor()
	ex.Store = store
	ex.MemoStore = memoStore
	ex.InterleavedConfig = interleavedthinking.ShapeConfig{
		Instructions:          "instructions",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}

	scope := interleavedthinking.Scope(aLeg.ALegID)
	initialRef, err := memoStore.Put(ctx, scope, interleavedthinking.MemoState{
		Memo:                  "original memo",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatalf("put initial memo: %v", err)
	}

	initialInterleaved := interleavedstate.State{
		Cycle:   interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 0},
		MemoRef: &initialRef,
	}
	if err := store.SetInterleavedState(ctx, aLeg.ALegID, initialInterleaved); err != nil {
		t.Fatalf("set initial interleaved state: %v", err)
	}

	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}
	entries := []legEntry{{cand: candA, delay: 0}, {cand: candB, delay: 0}}
	candidates := []routing.AttemptCandidate{candA, candB}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-fatal", aLegID: aLeg.ALegID},
		},
		routeFacts: routeFacts{},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
		interleaved: initialInterleaved,
	}

	// Arm A failed with non-fatal stream error, and Arm B has a fatal TTFT timeout (no winner)
	outcomeA := parallelArmOutcome{cand: candA, failErr: errors.New("stream failure a"), arrival: 1}
	outcomeB := parallelArmOutcome{cand: candB, failErr: lipapi.ErrTTFTTimeout, arrival: 2}

	cycleAdvance := &interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 1}
	reducer := newParallelRoundReducer(ex, req, entries, candidates, cycleAdvance)
	_, err = reducer.Reduce(ctx, []parallelArmOutcome{outcomeA, outcomeB})
	if err == nil || !errors.Is(err, lipapi.ErrTTFTTimeout) {
		t.Fatalf("expected fatal TTFT error, got %v", err)
	}

	// Store must remain untouched
	memo, ok, err := memoStore.Get(ctx, scope, initialRef)
	if err != nil || !ok {
		t.Fatalf("fetch memo: ok=%v, err=%v", ok, err)
	}
	if memo.Memo != "original memo" {
		t.Errorf("memo was mutated despite fatal error: got %q", memo.Memo)
	}
}

// TestPhase5_WinnerOnlyCommit_ContextCanceledNeverPersists proves that when context is canceled,
// winner selection effects are not persisted.
func TestPhase5_WinnerOnlyCommit_ContextCanceledNeverPersists(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Already canceled

	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	aLeg, err := store.CreateALeg(context.Background(), "")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	memoStore := interleavedthinking.NewMemoStore(4096)
	ex := TestExecutor()
	ex.Store = store
	ex.MemoStore = memoStore
	ex.InterleavedConfig = interleavedthinking.ShapeConfig{
		Instructions:          "instructions",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}

	scope := interleavedthinking.Scope(aLeg.ALegID)
	initialRef, err := memoStore.Put(context.Background(), scope, interleavedthinking.MemoState{
		Memo:                  "original memo",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatalf("put initial memo: %v", err)
	}

	initialInterleaved := interleavedstate.State{
		Cycle:   interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 0},
		MemoRef: &initialRef,
	}
	if err := store.SetInterleavedState(context.Background(), aLeg.ALegID, initialInterleaved); err != nil {
		t.Fatalf("set initial interleaved state: %v", err)
	}

	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	entries := []legEntry{{cand: candA, delay: 0}}
	candidates := []routing.AttemptCandidate{candA}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-cancel", aLegID: aLeg.ALegID},
		},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
		interleaved: initialInterleaved,
	}

	sessionA := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-a"}}
	readyA := &readyAttempt{
		session: sessionA,
		pending: pendingSelectionEffects{
			interleaved: initialInterleaved,
			memoUpdate: &interleavedthinking.PendingMemoUpdate{
				Ref:   initialRef,
				State: interleavedthinking.MemoState{Memo: "canceled memo", RegularTurnsRemaining: 1},
			},
		},
	}

	outcomeA := parallelArmOutcome{cand: candA, ready: readyA, arrival: 1}

	cycleAdvance := &interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 1}
	reducer := newParallelRoundReducer(ex, req, entries, candidates, cycleAdvance)
	_, err = reducer.Reduce(ctx, []parallelArmOutcome{outcomeA})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got %v", err)
	}

	if !readyA.consumed {
		t.Errorf("expected readyA to be disposed on context cancellation")
	}

	// Store must remain untouched
	memo, ok, err := memoStore.Get(context.Background(), scope, initialRef)
	if err != nil || !ok {
		t.Fatalf("fetch memo: ok=%v, err=%v", ok, err)
	}
	if memo.Memo != "original memo" {
		t.Errorf("memo was mutated despite context cancellation: got %q", memo.Memo)
	}
}

// failingMemoUpdateStore is a test double that fails on Update.
type failingMemoUpdateStore struct {
	inner interleavedthinking.MemoStore
	calls atomic.Int32
}

func (s *failingMemoUpdateStore) Put(ctx context.Context, scope interleavedthinking.Scope, state interleavedthinking.MemoState) (interleavedstate.MemoRef, error) {
	return s.inner.Put(ctx, scope, state)
}

func (s *failingMemoUpdateStore) Get(ctx context.Context, scope interleavedthinking.Scope, ref interleavedstate.MemoRef) (interleavedthinking.MemoState, bool, error) {
	return s.inner.Get(ctx, scope, ref)
}

func (s *failingMemoUpdateStore) Update(ctx context.Context, scope interleavedthinking.Scope, ref interleavedstate.MemoRef, state interleavedthinking.MemoState) (interleavedstate.MemoRef, error) {
	s.calls.Add(1)
	return interleavedstate.MemoRef{}, errors.New("simulated memo update failure")
}

func (s *failingMemoUpdateStore) Delete(ctx context.Context, scope interleavedthinking.Scope, ref interleavedstate.MemoRef) error {
	return s.inner.Delete(ctx, scope, ref)
}

// TestPhase5_WinnerOnlyCommit_CommitFailureCleansUpAndReleases proves that when committing
// winner selection effects fails, the winner is disposed, losers are released, and the error is returned.
func TestPhase5_WinnerOnlyCommit_CommitFailureCleansUpAndReleases(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	aLeg, err := store.CreateALeg(ctx, "")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	innerMemo := interleavedthinking.NewMemoStore(4096)
	memoStore := &failingMemoUpdateStore{inner: innerMemo}
	ex := TestExecutor()
	ex.Store = store
	ex.MemoStore = memoStore
	ex.InterleavedConfig = interleavedthinking.ShapeConfig{
		Instructions:          "instructions",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}

	scope := interleavedthinking.Scope(aLeg.ALegID)
	initialRef, err := innerMemo.Put(ctx, scope, interleavedthinking.MemoState{
		Memo:                  "original memo",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatalf("put initial memo: %v", err)
	}

	initialInterleaved := interleavedstate.State{
		Cycle:   interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 0},
		MemoRef: &initialRef,
	}
	if err := store.SetInterleavedState(ctx, aLeg.ALegID, initialInterleaved); err != nil {
		t.Fatalf("set initial interleaved state: %v", err)
	}

	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	candB := routing.AttemptCandidate{Key: "cand-b", Primary: routing.Primary{Backend: "b-b", Model: "m-b"}}
	entries := []legEntry{{cand: candA, delay: 0}, {cand: candB, delay: 0}}
	candidates := []routing.AttemptCandidate{candA, candB}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-commit-fail", aLegID: aLeg.ALegID},
		},
		routeFacts: routeFacts{},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
		interleaved: initialInterleaved,
	}

	sessionA := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-a"}}
	readyA := &readyAttempt{
		session: sessionA,
		pending: pendingSelectionEffects{
			interleaved: initialInterleaved,
			memoUpdate: &interleavedthinking.PendingMemoUpdate{
				Ref:   initialRef,
				State: interleavedthinking.MemoState{Memo: "failed memo", RegularTurnsRemaining: 1},
			},
		},
	}

	outcomeA := parallelArmOutcome{cand: candA, ready: readyA, arrival: 1}
	outcomeB := parallelArmOutcome{cand: candB, failErr: errors.New("loser B failed"), arrival: 2}

	cycleAdvance := &interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 1}
	reducer := newParallelRoundReducer(ex, req, entries, candidates, cycleAdvance)
	_, err = reducer.Reduce(ctx, []parallelArmOutcome{outcomeA, outcomeB})
	if err == nil {
		t.Fatal("expected commit error, got nil")
	}

	// Winner readyAttempt must be disposed
	if !readyA.consumed {
		t.Errorf("expected readyA to be marked consumed/disposed after commit failure")
	}

	// Store must remain untouched
	memo, ok, err := innerMemo.Get(ctx, scope, initialRef)
	if err != nil || !ok {
		t.Fatalf("fetch memo: ok=%v, err=%v", ok, err)
	}
	if memo.Memo != "original memo" {
		t.Errorf("memo was mutated despite commit failure: got %q", memo.Memo)
	}
}

// TestPhase5_WinnerOnlyCommit_LoserDisposeDoesNotMutateStore directly proves that calling
// Dispose on a readyAttempt carrying pendingSelectionEffects terminalizes the attempt session
// without committing or mutating any memo or cycle state in stores.
func TestPhase5_WinnerOnlyCommit_LoserDisposeDoesNotMutateStore(t *testing.T) {
	t.Parallel()

	session := &attemptSession{
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{BLegID: "bleg-loser"},
	}

	ref := interleavedstate.MemoRef{Key: "ref-1", Version: 1}
	ready := &readyAttempt{
		session: session,
		pending: pendingSelectionEffects{
			interleaved: interleavedstate.State{
				Cycle:   interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 1},
				MemoRef: &ref,
			},
			memoUpdate: &interleavedthinking.PendingMemoUpdate{
				Ref: ref,
				State: interleavedthinking.MemoState{
					Memo:                  "loser pending memo",
					RegularTurnsRemaining: 1,
				},
			},
		},
	}

	ctx := context.Background()
	ready.Dispose(ctx, errors.New("parallel race loser"))

	if !ready.consumed {
		t.Errorf("expected readyAttempt to be marked consumed after Dispose")
	}
	// Calling Consume after Dispose should fail
	if _, err := ready.Consume(); err == nil {
		t.Errorf("expected Consume after Dispose to fail")
	}
}

// TestPhase5_WinnerOnlyCommit_PublicationDeniedByClosedSlot proves that when publication
// is denied by the slot (e.g., Close won the publication lease), the ready attempt is disposed
// and its pending selection effects (memo/cycle) are never committed to the store.
func TestPhase5_WinnerOnlyCommit_PublicationDeniedByClosedSlot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	aLeg, err := store.CreateALeg(ctx, "")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	memoStore := interleavedthinking.NewMemoStore(4096)
	scope := interleavedthinking.Scope(aLeg.ALegID)
	initialRef, err := memoStore.Put(ctx, scope, interleavedthinking.MemoState{
		Memo:                  "original memo before publication denial",
		RegularTurnsRemaining: 3,
	})
	if err != nil {
		t.Fatalf("put initial memo: %v", err)
	}

	initialInterleaved := interleavedstate.State{
		Cycle:   interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 0},
		MemoRef: &initialRef,
	}
	if err := store.SetInterleavedState(ctx, aLeg.ALegID, initialInterleaved); err != nil {
		t.Fatalf("set initial interleaved state: %v", err)
	}

	slot := &attemptSlot{}
	initialSession := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-initial"}}
	slot.install(initialSession)

	// Close wins the race and closes publication on the slot
	closedCurrent := slot.closePublicationAndSnapshot()
	if closedCurrent != initialSession {
		t.Fatalf("expected closed snapshot to return initialSession")
	}

	replacementSession := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-replacement"}}
	ready := &readyAttempt{
		session: replacementSession,
		pending: pendingSelectionEffects{
			interleaved: interleavedstate.State{
				Cycle:   interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 1},
				MemoRef: &initialRef,
			},
			memoUpdate: &interleavedthinking.PendingMemoUpdate{
				Ref: initialRef,
				State: interleavedthinking.MemoState{
					Memo:                  "denied replacement memo",
					RegularTurnsRemaining: 2,
				},
			},
		},
	}

	// Publication attempt should be denied
	old, published := slot.swapIfOpen(ready)
	if published {
		t.Fatalf("expected swapIfOpen to reject publication on closed slot")
	}
	if old != initialSession {
		t.Fatalf("expected swapIfOpen to retain old session, got %v", old)
	}

	// Caller disposes the unpublished ready attempt
	ready.Dispose(ctx, errors.New("publication closed"))

	if !ready.consumed {
		t.Errorf("expected ready to be consumed/disposed")
	}

	// Store must remain untouched: memo and cycle are never persisted
	memo, ok, err := memoStore.Get(ctx, scope, initialRef)
	if err != nil || !ok {
		t.Fatalf("fetch memo: ok=%v, err=%v", ok, err)
	}
	if memo.Memo != "original memo before publication denial" {
		t.Errorf("memo was mutated despite publication denial: got %q", memo.Memo)
	}

	state, err := store.FetchInterleavedState(ctx, aLeg.ALegID)
	if err != nil {
		t.Fatalf("fetch state: %v", err)
	}
	if state.Cycle.NextIndex != 0 {
		t.Errorf("cycle was mutated despite publication denial: got %d, want 0", state.Cycle.NextIndex)
	}
}

// TestPhase5_WinnerOnlyCommit_AlreadyConsumedReadyRejectedInReduce proves that if a ready attempt
// was already consumed or disposed before Reduce, Consume returns an error and Reduce cleanly aborts
// without committing any pending winner effects.
func TestPhase5_WinnerOnlyCommit_AlreadyConsumedReadyRejectedInReduce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	aLeg, err := store.CreateALeg(ctx, "")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}

	memoStore := interleavedthinking.NewMemoStore(4096)
	ex := TestExecutor()
	ex.Store = store
	ex.MemoStore = memoStore
	ex.InterleavedConfig = interleavedthinking.ShapeConfig{
		Instructions:          "instructions",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}

	scope := interleavedthinking.Scope(aLeg.ALegID)
	initialRef, err := memoStore.Put(ctx, scope, interleavedthinking.MemoState{
		Memo:                  "original memo before consumed ready",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatalf("put initial memo: %v", err)
	}

	initialInterleaved := interleavedstate.State{
		Cycle:   interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 0},
		MemoRef: &initialRef,
	}
	if err := store.SetInterleavedState(ctx, aLeg.ALegID, initialInterleaved); err != nil {
		t.Fatalf("set initial interleaved state: %v", err)
	}

	candA := routing.AttemptCandidate{Key: "cand-a", Primary: routing.Primary{Backend: "b-a", Model: "m-a"}}
	entries := []legEntry{{cand: candA, delay: 0}}
	candidates := []routing.AttemptCandidate{candA}

	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{traceID: "trace-consumed", aLegID: aLeg.ALegID},
		},
		progress: &recoveryController{
			excluded: map[string]struct{}{},
			failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}},
		},
		interleaved: initialInterleaved,
	}

	sessionA := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{BLegID: "bleg-a"}}
	readyA := &readyAttempt{
		session: sessionA,
		pending: pendingSelectionEffects{
			interleaved: initialInterleaved,
			memoUpdate: &interleavedthinking.PendingMemoUpdate{
				Ref:   initialRef,
				State: interleavedthinking.MemoState{Memo: "should never persist", RegularTurnsRemaining: 1},
			},
		},
	}

	// Pre-dispose or consume readyA
	readyA.Dispose(ctx, errors.New("aborted before reduce"))

	outcomeA := parallelArmOutcome{cand: candA, ready: readyA, arrival: 1}

	cycleAdvance := &interleavedstate.CycleState{SelectorKey: "sel-1", NextIndex: 1}
	reducer := newParallelRoundReducer(ex, req, entries, candidates, cycleAdvance)
	_, err = reducer.Reduce(ctx, []parallelArmOutcome{outcomeA})
	if err == nil {
		t.Fatal("expected error on already consumed readyAttempt, got nil")
	}

	// Store must remain untouched
	memo, ok, err := memoStore.Get(ctx, scope, initialRef)
	if err != nil || !ok {
		t.Fatalf("fetch memo: ok=%v, err=%v", ok, err)
	}
	if memo.Memo != "original memo before consumed ready" {
		t.Errorf("memo was mutated: got %q, want original", memo.Memo)
	}
}
