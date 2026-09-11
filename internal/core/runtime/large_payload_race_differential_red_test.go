package runtime

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
)

// scheduledEvent represents a timed event emission from a test stream.
type scheduledEvent struct {
	delay time.Duration
	event lipapi.Event
}

// delayedEventsStream simulates an event stream with scheduled emission delays.
type delayedEventsStream struct {
	ctx      context.Context
	schedule []scheduledEvent
	mu       sync.Mutex
	idx      int
	closed   bool
}

func (s *delayedEventsStream) Recv(ctx context.Context) (lipapi.Event, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return lipapi.Event{}, io.EOF
	}
	if s.idx >= len(s.schedule) {
		s.mu.Unlock()
		return lipapi.Event{}, io.EOF
	}
	item := s.schedule[s.idx]
	s.idx++
	s.mu.Unlock()

	if item.delay > 0 {
		select {
		case <-time.After(item.delay):
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		case <-s.ctx.Done():
			return lipapi.Event{}, s.ctx.Err()
		}
	}
	return item.event, nil
}

func (s *delayedEventsStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

var _ lipapi.EventStream = (*delayedEventsStream)(nil)

func makeDelayedManagedStream(ctx context.Context, schedule []scheduledEvent) lipapi.ManagedEventStream {
	return lipapi.CloseOnlyManagedStream{
		Stream: &delayedEventsStream{
			ctx:      ctx,
			schedule: schedule,
		},
	}
}

// hangingLoserStream ignores context cancellation on Recv and blocks until unblocked.
type hangingLoserStream struct {
	blockCh chan struct{}
}

func (s *hangingLoserStream) Recv(ctx context.Context) (lipapi.Event, error) {
	<-s.blockCh
	return lipapi.Event{}, io.EOF
}

func (s *hangingLoserStream) Close() error {
	return nil
}

var _ lipapi.EventStream = (*hangingLoserStream)(nil)

func makeHangingManagedStream(blockCh chan struct{}) lipapi.ManagedEventStream {
	return lipapi.CloseOnlyManagedStream{
		Stream: &hangingLoserStream{blockCh: blockCh},
	}
}

// faultStore wraps b2bua.Store and injects errors into SetWeightedFirstConsumed.
type faultStore struct {
	b2bua.Store
	mu                          sync.Mutex
	setWeightedFirstConsumedErr error
	consumedCalls               int
}

func (s *faultStore) SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error {
	s.mu.Lock()
	s.consumedCalls++
	err := s.setWeightedFirstConsumedErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.Store.SetWeightedFirstConsumed(ctx, aLegID, consumed)
}

// TestFindingB2a_RaceWinnerRequiresTextOrReasoningDeltaNotMessageStarted proves Finding B2a:
// In canonical parallel race (parallel_race.go:939-941 isWinningEvent), an arm only wins when
// it emits non-empty EventTextDelta or EventReasoningDelta. Leading metadata events (MessageStarted)
// are pre-buffered and do NOT decide the race.
//
// Original wire defect (regression guard):
// executor_execute_large_body.go:1467 calls peekFirstWireEvent and lines 1496-1506 pick the FIRST
// arm to emit ANY event (even MessageStarted) as the winner.
//
// Test scenario:
// Arm A: MessageStarted @30ms, text @400ms.
// Arm B: MessageStarted @60ms, text @90ms.
// Canonical winner: Arm B (first text at 90ms vs 400ms).
// Wire winner today: Arm A (first event MessageStarted at 30ms vs 60ms).
func TestFindingB2a_RaceWinnerRequiresTextOrReasoningDeltaNotMessageStarted(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"race-meta"}]}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	acc.WireRequest.CandidateModel = "b1:m1!b2:m2"

	ex.Backends = map[string]execbackend.Backend{
		"b1": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
				return makeDelayedManagedStream(ctx, []scheduledEvent{
					{delay: 30 * time.Millisecond, event: lipapi.Event{Kind: lipapi.EventMessageStarted}},
					{delay: 370 * time.Millisecond, event: lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "a-text"}},
					{delay: 10 * time.Millisecond, event: lipapi.Event{Kind: lipapi.EventResponseFinished}},
				}), nil
			},
		},
		"b2": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
				return makeDelayedManagedStream(ctx, []scheduledEvent{
					{delay: 60 * time.Millisecond, event: lipapi.Event{Kind: lipapi.EventMessageStarted}},
					{delay: 30 * time.Millisecond, event: lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "b-text"}},
					{delay: 10 * time.Millisecond, event: lipapi.Event{Kind: lipapi.EventResponseFinished}},
				}), nil
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-race-meta"})
	ctx = largebody.WithWireIdentity(ctx, "req-race-meta", "trace-race-meta")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	defer res.Stream.Close()

	var firstText string
	for {
		ev, rerr := res.Stream.Recv(ctx)
		if rerr != nil {
			break
		}
		if ev.Kind == lipapi.EventTextDelta && ev.Delta != "" {
			firstText = ev.Delta
			break
		}
	}
	assert.Equal(t, "b-text", firstText,
		"canonical parallel race winner must be Arm B (first arm emitting text delta), but wire picked Arm A on metadata event MessageStarted")
}

// TestFindingB2b_ParallelRaceHonorsHandicapDelays proves Finding B2b:
// Canonical parallel race (parallel_race.go:185-192, 258-273) sorts arms by handicap and enforces
// delay timers (delay = maxHandicap - cand.Handicap).
//
// Original wire defect (regression guard):
// executeWireParallelRace (executor_execute_large_body.go:1286-1370) completely ignores cand.Handicap,
// launching all candidates simultaneously with zero delay.
//
// Test scenario:
// Selector: b1:m1![handicap=1]b2:m2
// Arm b2 has handicap 1s (launches at t=0, delay=0). Responds at t=80ms.
// Arm b1 has handicap 0 (delay = 1s). Fast arm (latency 30ms), but must not launch until 1s.
// Canonical winner: b2 (finishes at 80ms while b1 is delayed).
// Wire winner today: b1 (launches immediately with no delay, finishes at 30ms).
func TestFindingB2b_ParallelRaceHonorsHandicapDelays(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"race-handicap"}]}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	acc.WireRequest.CandidateModel = "b1:m1![handicap=1]b2:m2"

	ex.Backends = map[string]execbackend.Backend{
		"b1": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
				return makeDelayedManagedStream(ctx, []scheduledEvent{
					{delay: 30 * time.Millisecond, event: lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "b1-text"}},
					{delay: 10 * time.Millisecond, event: lipapi.Event{Kind: lipapi.EventResponseFinished}},
				}), nil
			},
		},
		"b2": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
				return makeDelayedManagedStream(ctx, []scheduledEvent{
					{delay: 80 * time.Millisecond, event: lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "b2-text"}},
					{delay: 10 * time.Millisecond, event: lipapi.Event{Kind: lipapi.EventResponseFinished}},
				}), nil
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-race-handicap"})
	ctx = largebody.WithWireIdentity(ctx, "req-race-handicap", "trace-race-handicap")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	defer res.Stream.Close()

	ev, err := res.Stream.Recv(ctx)
	require.NoError(t, err)
	assert.Equal(t, "b2-text", ev.Delta,
		"handicapped candidate b2 must win because b1 has 1s handicap delay, but wire ignored handicap and picked b1")
}

// TestFindingB2c_WinnerNotHostageToLoserCancellation proves Finding B2c:
// Canonical parallel race (parallel_race.go:120-171, 480-499) publishes the winner decision immediately
// to decisionCh and detaches loser cleanup asynchronously. The caller gets the winner without waiting
// for unresponsive losers.
//
// Original wire defect (regression guard):
// executor_execute_large_body.go:1486-1496 does `wg.Wait(); close(ch)` and loops `for res := range ch`.
// When a loser hangs and ignores context cancellation, wg.Wait() blocks forever, holding the winner
// hostage to the hanging loser.
func TestFindingB2c_WinnerNotHostageToLoserCancellation(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"race-hostage"}]}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	acc.WireRequest.CandidateModel = "winner:m!loser:m"

	loserBlock := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-loserBlock:
		default:
			close(loserBlock)
		}
	})

	ex.Backends = map[string]execbackend.Backend{
		"winner": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
				return makeStreamWithEvents(
					lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "winner-text"},
					lipapi.Event{Kind: lipapi.EventResponseFinished},
				), nil
			},
		},
		"loser": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
				return makeHangingManagedStream(loserBlock), nil
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-race-hostage"})
	ctx = largebody.WithWireIdentity(ctx, "req-race-hostage", "trace-race-hostage")

	done := make(chan struct{})
	var res largebody.ExecutionResult
	var execErr error

	go func() {
		res, execErr = ex.ExecuteLargeBody(ctx, acc, src)
		close(done)
	}()

	select {
	case <-done:
		require.NoError(t, execErr)
		close(loserBlock)
		_ = res.Stream.Close()
	case <-time.After(300 * time.Millisecond):
		t.Fatal("ExecuteLargeBody timed out: winner is held hostage to hanging loser (loser cleanup not detached from winner return)")
	}
}

// TestFindingB2d_BackendPanicInParallelArmIsolated proves Finding B2d:
// Canonical parallel race (parallel_race.go:329-337, 454-470) wraps arm execution in `recover()`
// and captures the panic with safety.Capture, isolating it so other arms survive and the process stays alive.
//
// Original wire defect (regression guard):
// executor_execute_large_body.go:1390-1483 launches bare `go func(idx int)` around OpenWire and Recv
// with NO panic recovery. A backend panic in any parallel arm crashes the entire process.
func TestFindingB2d_BackendPanicInParallelArmIsolated(t *testing.T) {
	if os.Getenv("TEST_PANIC_ISOLATION_HELPER") == "1" {
		runPanicIsolationHelper(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestFindingB2d_BackendPanicInParallelArmIsolated$")
	cmd.Env = append(os.Environ(), "TEST_PANIC_ISOLATION_HELPER=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err,
		"backend panic in parallel race arm must be isolated and not crash the process:\n%s", string(out))
}

func runPanicIsolationHelper(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"panic-arm"}]}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	acc.WireRequest.CandidateModel = "panicker:m!survivor:m"

	ex.Backends = map[string]execbackend.Backend{
		"panicker": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
				panic("injected backend crash in parallel arm")
			},
		},
		"survivor": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
				return makeStreamWithEvents(
					lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "survivor-text"},
					lipapi.Event{Kind: lipapi.EventResponseFinished},
				), nil
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-panic"})
	ctx = largebody.WithWireIdentity(ctx, "req-panic", "trace-panic")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	require.NoError(t, err)
	_ = res.Stream.Close()
}

// TestFindingH3_WeightedFirstTwoTurnSequentialAndParallel proves Finding H3 (e):
// When a route candidate has [first] / MarkedFirst, winning the attempt must authoritative commit
// SetWeightedFirstConsumed(ctx, aLegID, true).
//
// Original wire defect (regression guard):
// 1. executor_execute_large_body.go:1242 only calls SetWeightedFirstConsumed if `!c.IsParallel`.
// 2. executeWireParallelRace NEVER calls SetWeightedFirstConsumed on the parallel race winner.
// As a result, subsequent turns on the same session see WeightedFirstConsumed=false and route
// to [first] again instead of advancing to weighted alternatives.
func TestFindingH3_WeightedFirstTwoTurnSequentialAndParallel(t *testing.T) {
	t.Run("parallel_winner_must_update_weighted_first_consumed", func(t *testing.T) {
		ex, _, store := setupTestExecutor(t)
		src := newTestSource(`{"model":"gpt-4o","prompt":"parallel-first"}`)
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

		ex.Backends = map[string]execbackend.Backend{
			"first_be": {
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
					return makeStreamWithEvents(
						lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "first-response"},
						lipapi.Event{Kind: lipapi.EventResponseFinished},
					), nil
				},
			},
			"alt_be": {
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
					return makeDelayedManagedStream(ctx, []scheduledEvent{
						{delay: 40 * time.Millisecond, event: lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "alt-response"}},
						{delay: 10 * time.Millisecond, event: lipapi.Event{Kind: lipapi.EventResponseFinished}},
					}), nil
				},
			},
		}

		ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-par-first"})
		ctx = largebody.WithWireIdentity(ctx, "req-par-first", "trace-par-first")

		// Create A-leg in store
		aLegRecord, err := store.CreateALeg(ctx, "continuity-par-first")
		require.NoError(t, err)
		require.False(t, aLegRecord.WeightedFirstConsumed, "initial WeightedFirstConsumed must be false")

		aScope := ex.lifecycleCoordinator().StartALeg(aLegRecord.ALegID)
		defer aScope.End()

		candidates := []routing.AttemptCandidate{
			{
				Primary:     routing.Primary{Backend: "first_be", Model: "m"},
				Key:         "first_be:m",
				IsParallel:  true,
				MarkedFirst: true, // parallel candidate marked [first]
			},
			{
				Primary:    routing.Primary{Backend: "alt_be", Model: "m"},
				Key:        "alt_be:m",
				IsParallel: true,
			},
		}

		budget := &attemptBudget{max: 5}
		failures := &candidateFailureHistory{}
		excluded := map[string]struct{}{}
		ttft := newTTFTBudget(time.Now(), nil)

		in := wireAttemptInput{
			ctx:                   ctx,
			outCtx:                ctx,
			accepted:              acc,
			src:                   src,
			traceID:               "trace-par-first",
			aLegID:                aLegRecord.ALegID,
			aScope:                aScope,
			weightedFirstConsumed: false,
		}

		res, err := ex.executeWireParallelRace(in, candidates, budget, failures, excluded, ttft, affinity.Key{}, false)
		require.NoError(t, err)
		require.True(t, res.opened)
		_ = res.stream.Close()

		// Invariant: parallel race winner marked with [first] must commit WeightedFirstConsumed=true
		updatedRecord, err := store.FetchALeg(ctx, aLegRecord.ALegID)
		require.NoError(t, err)
		assert.True(t, updatedRecord.WeightedFirstConsumed,
			"parallel race winner with MarkedFirst=true must update WeightedFirstConsumed in store, but wire skipped it")

		// Turn 2 routing verification: routing must reflect consumed state
		sel, err := routing.Parse("[first]first_be:m^[weight=100]alt_be:m")
		require.NoError(t, err)
		sessionState := &routing.SessionRoutingState{FirstRequestConsumed: updatedRecord.WeightedFirstConsumed}
		groups, err := routing.ExpandFailoverGroups(sel, routing.PlanOptions{
			Session: sessionState,
		})
		require.NoError(t, err)
		require.NotEmpty(t, groups)
		require.NotEmpty(t, groups[0].Candidates)
		assert.Equal(t, "alt_be:m", groups[0].Candidates[0].Key,
			"turn 2 must advance to alt_be because [first] was consumed by parallel winner, but still routed to first_be")
	})

	t.Run("sequential_two_turn_advances_from_first", func(t *testing.T) {
		ex, _, store := setupTestExecutor(t)
		src := newTestSource(`{"model":"gpt-4o","prompt":"seq-first"}`)
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
		acc.WireRequest.CandidateModel = "[first]be1:m^[weight=100]be2:m"

		var be1Calls, be2Calls int
		ex.Backends = map[string]execbackend.Backend{
			"be1": {
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
					be1Calls++
					return makeStreamWithEvents(lipapi.Event{Kind: lipapi.EventResponseFinished}), nil
				},
			},
			"be2": {
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
					be2Calls++
					return makeStreamWithEvents(lipapi.Event{Kind: lipapi.EventResponseFinished}), nil
				},
			},
		}

		ctx1 := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-seq-first"})
		ctx1 = largebody.WithWireIdentity(ctx1, "req-turn-1", "trace-turn-1")

		// Turn 1
		res1, err := ex.ExecuteLargeBody(ctx1, acc, src)
		require.NoError(t, err)
		_ = res1.Stream.Close()

		assert.Equal(t, 1, be1Calls, "turn 1 must pick [first] branch be1")
		assert.Equal(t, 0, be2Calls, "turn 1 must not pick be2")

		// Turn 2 on the same session
		resumeToken := res1.Session.ResumeToken.Reveal()
		ctx2 := largebody.WithWireIdentity(ctx1, "req-turn-2", "trace-turn-2")
		ctx2 = largebody.WithWireSessionInput(ctx2, largebody.SessionInput{
			AuthoritativeSessionID: res1.Session.AuthoritativeSessionID,
			ResumeToken:            largebody.NewSensitiveString(resumeToken),
		})

		res2, err := ex.ExecuteLargeBody(ctx2, acc, src)
		require.NoError(t, err)
		_ = res2.Stream.Close()

		// Verify A-leg record in store
		aLegRec, err := store.FetchALeg(ctx1, res1.Facts.ALegID)
		require.NoError(t, err)
		assert.True(t, aLegRec.WeightedFirstConsumed, "A-leg WeightedFirstConsumed must be true after turn 1")

		assert.Equal(t, 1, be2Calls, "turn 2 must advance to be2 because [first] was consumed")
	})
}

// TestFindingH3_StoreSetWeightedFirstConsumedFailureAborts proves Finding H3 (f):
// In canonical execution (executor_open_attempt.go:914, parallel_race.go:788), an error from
// Store.SetWeightedFirstConsumed is treated as authoritative and causes the attempt to abort.
//
// Original wire defect (regression guard):
// executor_execute_large_body.go:1243 does `_ = e.Store.SetWeightedFirstConsumed(...)` which swallows
// the store error and commits the attempt anyway.
func TestFindingH3_StoreSetWeightedFirstConsumedFailureAborts(t *testing.T) {
	ex, _, baseStore := setupTestExecutor(t)
	injectedErr := errors.New("injected SetWeightedFirstConsumed storage failure")
	fStore := &faultStore{
		Store:                       baseStore,
		setWeightedFirstConsumedErr: injectedErr,
	}
	ex.Store = fStore

	src := newTestSource(`{"model":"gpt-4o","prompt":"store-fault"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	acc.WireRequest.CandidateModel = "[first]be1:m^[weight=100]be2:m"

	ex.Backends = map[string]execbackend.Backend{
		"be1": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
				return makeStreamWithEvents(lipapi.Event{Kind: lipapi.EventResponseFinished}), nil
			},
		},
		"be2": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
				return makeStreamWithEvents(lipapi.Event{Kind: lipapi.EventResponseFinished}), nil
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-fault"})
	ctx = largebody.WithWireIdentity(ctx, "req-fault", "trace-fault")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err == nil && res.Stream != nil {
		_ = res.Stream.Close()
	}

	// Invariant: storage failure on SetWeightedFirstConsumed must abort execution
	require.Error(t, err, "execution must abort when SetWeightedFirstConsumed fails, but wire swallowed error")
	assert.ErrorIs(t, err, injectedErr, "expected returned error to wrap injected SetWeightedFirstConsumed failure")
}
