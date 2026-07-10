package runtime

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

// This file pins usage-authority reservation cleanup on the parallel-race failure paths
// (L2/L3/L4/L5) and documents the intentional attempt-budget no-refund behavior (L10).
// It drives Executor.tryOpenParallelGroup directly so the leak sites are exercised in
// isolation, reusing the recordingAuthorityService / authorityOpenParams helpers.

// reservedAuthorityRecorder builds a recordingAuthorityService that always admits and
// reserves with the supplied reservation id, so each opened leg holds a releasable
// reservation that the tests can assert on.
func reservedAuthorityRecorder(reservationID string) *recordingAuthorityService {
	return &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  reservationID,
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
}

// blockingRecvStream never produces events; Recv blocks until ctx is done. Used to keep
// a leg parked in its receive loop so a global TTFT deadline (L3) or an external ctx
// cancel (L4) can fire against an already-opened leg.
type blockingRecvStream struct{}

func (blockingRecvStream) Recv(ctx context.Context) (lipapi.Event, error) {
	<-ctx.Done()
	return lipapi.Event{}, ctx.Err()
}

func (blockingRecvStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{}
}

func (blockingRecvStream) Close() error { return nil }

// signalOnceBlockStream signals openedCh on its first Recv (which happens only after the
// leg has registered its B-leg and stored its authority), then blocks until ctx is done.
// Used by L4 to deterministically know a leg has opened before canceling the race ctx.
type signalOnceBlockStream struct {
	openedCh chan<- struct{}
	signaled bool
}

func (s *signalOnceBlockStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if !s.signaled {
		s.signaled = true
		select {
		case s.openedCh <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
	return lipapi.Event{}, ctx.Err()
}

func (s *signalOnceBlockStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{}
}

func (s *signalOnceBlockStream) Close() error { return nil }

// waitThenWinStream blocks its first Recv until waitCh is closed, then yields the supplied
// events. Used by L5 so the winner cannot win until the loser has already opened.
type waitThenWinStream struct {
	waitCh <-chan struct{}
	events []lipapi.Event
	idx    int
}

func (s *waitThenWinStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.idx == 0 {
		select {
		case <-s.waitCh:
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		}
	}
	if s.idx >= len(s.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *waitThenWinStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{}
}

func (s *waitThenWinStream) Close() error { return nil }

// failingUpdateMemoStore delegates Get/Put/Delete to a real in-memory store (so executor
// memo shaping produces a non-nil PendingMemoUpdate) but always fails Update, forcing the
// commitMemoInjection failure path after a parallel winner is elected.
type failingUpdateMemoStore struct {
	inner       interleavedthinking.MemoStore
	updateCalls atomic.Int32
}

func (s *failingUpdateMemoStore) Put(ctx context.Context, scope interleavedthinking.Scope, state interleavedthinking.MemoState) (interleavedstate.MemoRef, error) {
	return s.inner.Put(ctx, scope, state)
}

func (s *failingUpdateMemoStore) Get(ctx context.Context, scope interleavedthinking.Scope, ref interleavedstate.MemoRef) (interleavedthinking.MemoState, bool, error) {
	return s.inner.Get(ctx, scope, ref)
}

func (s *failingUpdateMemoStore) Update(context.Context, interleavedthinking.Scope, interleavedstate.MemoRef, interleavedthinking.MemoState) (interleavedstate.MemoRef, error) {
	s.updateCalls.Add(1)
	return interleavedstate.MemoRef{}, errParallelRaceMemoUpdateFailed
}

func (s *failingUpdateMemoStore) Delete(ctx context.Context, scope interleavedthinking.Scope, ref interleavedstate.MemoRef) error {
	return s.inner.Delete(ctx, scope, ref)
}

var errParallelRaceMemoUpdateFailed = errors.New("parallel race memo update failed")

// parallelTransportCaps returns the streaming transport caps used by the authority test
// executor for backend-1, reused when extra backends are added for multi-leg tests.
func parallelTransportCaps() lipapi.BackendTransportCaps {
	return lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
		Operation: lipapi.OperationOpenAIChatCompletions,
		Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
	})
}

// runRaceInGoroutine runs tryOpenParallelGroup in a goroutine and returns its error,
// failing the test if it does not return within the timeout. Used by tests that drive the
// race against a real timer or external cancel.
func runRaceInGoroutine(t *testing.T, timeout time.Duration, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatalf("tryOpenParallelGroup did not return within %s", timeout)
		return nil
	}
}

// TestParallelRaceAuthorityLeak_L2_RegisterBLegFailureReleasesAuthority pins L2: when a
// per-leg RegisterBLeg fails (here because the A-leg is canceled inside backend Open), the
// leg goroutine must release the just-opened attempt's authority reservation before
// returning. legs[idx].authority is not assigned until after RegisterBLeg succeeds, so the
// release must target the local out.authority.
func TestParallelRaceAuthorityLeak_L2_RegisterBLegFailureReleasesAuthority(t *testing.T) {
	t.Parallel()

	auth := reservedAuthorityRecorder("res-l2")
	ex, _, backend, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	// Canceling the A-leg inside Open makes the subsequent RegisterBLeg fail with
	// ErrALegCanceled, which is the fatal-in-leg branch that previously leaked out.authority.
	backend.openFn = func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		_ = coord.CancelALeg(ctx, aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit})
		return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
	}

	p := authorityOpenParams(t, aLegID, &attemptBudget{max: 10})
	p.aScope = aScope

	err := runRaceInGoroutine(t, 5*time.Second, func() error {
		_, err := ex.tryOpenParallelGroup(p, []routing.AttemptCandidate{authorityCandidate()}, nil, "", false)
		return err
	})
	if err == nil {
		t.Fatal("expected parallel race error from RegisterBLeg failure")
	}

	if got := auth.releaseCalls.Load(); got != 1 {
		t.Fatalf("release calls = %d, want 1 (out.authority must be released on RegisterBLeg failure)", got)
	}
	release := auth.lastRelease()
	if release.ReservationID != "res-l2" {
		t.Fatalf("release reservation ID = %q, want res-l2", release.ReservationID)
	}
	if release.Kind != authorityapp.ReleaseKindLosing {
		t.Fatalf("release kind = %q, want losing", release.Kind)
	}
}

// TestParallelRaceAuthorityLeak_L3_FatalLegErrorReleasesOpenedLegs pins L3: when a leg
// raises a fatal error (global TTFT timeout) after at least one leg has opened, the
// fatal return path must release the authority of every opened leg. The opened leg is kept
// parked in its receive loop until the global TTFT deadline fires ErrTTFTTimeout.
func TestParallelRaceAuthorityLeak_L3_FatalLegErrorReleasesOpenedLegs(t *testing.T) {
	t.Parallel()

	auth := reservedAuthorityRecorder("res-l3")
	ex, _, backend, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	backend.openFn = func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		return blockingRecvStream{}, nil
	}

	p := authorityOpenParams(t, aLegID, &attemptBudget{max: 10})
	p.aScope = aScope
	// A short global TTFT deadline makes the parked leg's Recv return DeadlineExceeded,
	// which the race maps to a fatal ErrTTFTTimeout.
	p.ttft = &ttftBudget{start: time.Now(), global: 80 * time.Millisecond}

	err := runRaceInGoroutine(t, 5*time.Second, func() error {
		_, err := ex.tryOpenParallelGroup(p, []routing.AttemptCandidate{authorityCandidate()}, nil, "", false)
		return err
	})
	if err == nil {
		t.Fatal("expected parallel race fatal error from global TTFT timeout")
	}
	if !errors.Is(err, lipapi.ErrTTFTTimeout) {
		t.Fatalf("error = %v, want ErrTTFTTimeout", err)
	}

	if got := auth.releaseCalls.Load(); got != 1 {
		t.Fatalf("release calls = %d, want 1 (opened leg authority must be released on fatal abort)", got)
	}
	release := auth.lastRelease()
	if release.ReservationID != "res-l3" {
		t.Fatalf("release reservation ID = %q, want res-l3", release.ReservationID)
	}
	if release.Kind != authorityapp.ReleaseKindLosing {
		t.Fatalf("release kind = %q, want losing", release.Kind)
	}
}

// TestParallelRaceAuthorityLeak_L4_CtxDoneReleasesOpenedLegs pins L4: when the race ctx is
// canceled while at least one leg has already opened, the ctx.Done return path must
// defensively release the opened legs' authority and close their streams. Determinism is
// achieved by signaling from the leg's first Recv (which runs only after the leg has stored
// its authority) before canceling ctx, so ctx.Done is the only ready select case.
func TestParallelRaceAuthorityLeak_L4_CtxDoneReleasesOpenedLegs(t *testing.T) {
	t.Parallel()

	auth := reservedAuthorityRecorder("res-l4")
	ex, _, backend, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	openedCh := make(chan struct{}, 1)
	backend.openFn = func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		return &signalOnceBlockStream{openedCh: openedCh}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := authorityOpenParams(t, aLegID, &attemptBudget{max: 10})
	p.ctx = ctx
	p.aScope = aScope

	done := make(chan error, 1)
	go func() {
		_, err := ex.tryOpenParallelGroup(p, []routing.AttemptCandidate{authorityCandidate()}, nil, "", false)
		done <- err
	}()

	// Wait until the leg has reached Recv, which only happens after RegisterBLeg succeeded
	// and legs[idx].authority was stored.
	select {
	case <-openedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("leg never reached its receive loop; cannot exercise ctx.Done cleanup")
	}

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("race error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tryOpenParallelGroup did not return after ctx cancel")
	}

	if got := auth.releaseCalls.Load(); got != 1 {
		t.Fatalf("release calls = %d, want 1 (opened leg authority must be released on ctx.Done)", got)
	}
	release := auth.lastRelease()
	if release.ReservationID != "res-l4" {
		t.Fatalf("release reservation ID = %q, want res-l4", release.ReservationID)
	}
	if release.Kind != authorityapp.ReleaseKindLosing {
		t.Fatalf("release kind = %q, want losing", release.Kind)
	}
}

// TestParallelRaceAuthorityLeak_L5_CommitMemoInjectionFailureReleasesAuthority pins L5:
// when commitMemoInjection fails after a winner is elected, the cleanup must release the
// authority of the winner and every opened loser, alongside the existing cancelLosers and
// releaseBLegs calls. The loser is forced to open before the winner wins so both hold
// releasable reservations.
func TestParallelRaceAuthorityLeak_L5_CommitMemoInjectionFailureReleasesAuthority(t *testing.T) {
	t.Parallel()

	auth := reservedAuthorityRecorder("res-l5")
	ex, _, _, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	innerMemo := interleavedthinking.NewMemoStore(4096)
	memoStore := &failingUpdateMemoStore{inner: innerMemo}
	ex.MemoStore = memoStore
	ex.InterleavedConfig = interleavedthinking.ShapeConfig{
		Instructions:          "Think step by step.",
		StreamToClient:        "hidden",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}

	loserOpenedCh := make(chan struct{}, 1)
	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
	ex.Backends["loser"] = execbackend.Backend{
		Caps:          caps,
		TransportCaps: parallelTransportCaps(),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return &signalOnceBlockStream{openedCh: loserOpenedCh}, nil
		},
	}
	ex.Backends["winner"] = execbackend.Backend{
		Caps:          caps,
		TransportCaps: parallelTransportCaps(),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return &waitThenWinStream{
				waitCh: loserOpenedCh,
				events: []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "fast"}},
			}, nil
		},
	}

	ctx := context.Background()
	memoRef, err := innerMemo.Put(ctx, interleavedthinking.Scope(aLegID), interleavedthinking.MemoState{
		Memo:                  "parallel plan",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatalf("seed memo: %v", err)
	}

	loserCand := routing.AttemptCandidate{
		Primary:         routing.Primary{Backend: "loser", Model: "m"},
		Key:             "loser:m",
		InterleavedRole: interleavedstate.RoleExecutor,
	}
	winnerCand := routing.AttemptCandidate{
		Primary:         routing.Primary{Backend: "winner", Model: "m"},
		Key:             "winner:m",
		InterleavedRole: interleavedstate.RoleExecutor,
	}

	p := authorityOpenParams(t, aLegID, &attemptBudget{max: 10})
	p.ctx = ctx
	p.aScope = aScope
	p.interleaved = interleavedstate.State{MemoRef: &memoRef}
	p.baseline.Messages = []lipapi.Message{{
		Role:  lipapi.RoleUser,
		Parts: []lipapi.Part{lipapi.TextPart("hello")},
	}}

	raceErr := runRaceInGoroutine(t, 5*time.Second, func() error {
		_, err := ex.tryOpenParallelGroup(p, []routing.AttemptCandidate{loserCand, winnerCand}, nil, "", false)
		return err
	})
	if raceErr == nil {
		t.Fatal("expected commit memo injection failure to surface an error")
	}
	if !errors.Is(raceErr, errParallelRaceMemoUpdateFailed) {
		t.Fatalf("error = %v, want wrapped errParallelRaceMemoUpdateFailed", raceErr)
	}
	if memoStore.updateCalls.Load() == 0 {
		t.Fatal("memo Update was never called; commit path not exercised")
	}

	// Winner + the opened loser must both be released.
	if got := auth.releaseCalls.Load(); got != 2 {
		t.Fatalf("release calls = %d, want 2 (winner + opened loser)", got)
	}
	release := auth.lastRelease()
	if release.ReservationID != "res-l5" {
		t.Fatalf("release reservation ID = %q, want res-l5", release.ReservationID)
	}
	if release.Kind != authorityapp.ReleaseKindLosing {
		t.Fatalf("release kind = %q, want losing", release.Kind)
	}
}

// TestParallelRaceAuthorityLeak_L10_NoWinnerDoesNotRefundBudget pins L10: a parallel group
// that opens N legs up front and produces no winner intentionally does NOT refund the N
// attempt-budget slots. Every opened leg represents a genuine backend attempt, so counting
// them as consumed is correct back-pressure; refunding would under-count real attempts.
// This test documents that intent so a future "fix" does not silently change it.
func TestParallelRaceAuthorityLeak_L10_NoWinnerDoesNotRefundBudget(t *testing.T) {
	t.Parallel()

	// No usage authority: the legs never reserve, so the only observable side effect is
	// the attempt budget, which is acquired up front and intentionally not refunded.
	ex, _, backend, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, nil)

	openErr := errors.New("backend unavailable")
	backend.openFn = func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		return nil, openErr
	}
	ex.Backends["backend-2"] = execbackend.Backend{
		Caps:          lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		TransportCaps: parallelTransportCaps(),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return nil, openErr
		},
	}

	budget := &attemptBudget{max: 10}
	p := authorityOpenParams(t, aLegID, budget)

	cands := []routing.AttemptCandidate{
		{Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}, Key: "backend-1:model-1"},
		{Primary: routing.Primary{Backend: "backend-2", Model: "model-1"}, Key: "backend-2:model-1"},
	}

	_, err := ex.tryOpenParallelGroup(p, cands, nil, "", false)
	if err != nil {
		t.Fatalf("no-winner parallel race must not surface an error (it failovers), got: %v", err)
	}

	if got, want := budget.usedNow(), 2; got != want {
		t.Fatalf("budget used = %d, want %d (no-winner iteration intentionally consumes N slots as back-pressure)", got, want)
	}
}
