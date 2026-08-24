package runtime

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// TestAgentLoopGuard_Holdback_CandidateNeverReachesASideBeforeDecision
// Requirement 12.10: no provisional backend terminal shall become observable as final A-side terminal before guard decision.
// Uses entry channel instead of sleep for deterministic coordination.
func TestAgentLoopGuard_Holdback_CandidateNeverReachesASideBeforeDecision(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	block := make(chan struct{})
	fv := &fakeGuardVerifierWithBlock{
		enteredCh: entered,
		blockCh:   block,
		verdict:   stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "remaining work", Reason: "pending"},
	}
	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	done := make(chan struct{})
	var ev lipapi.Event
	var err error
	go func() {
		ev, err = testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw_backend_finish"})
		close(done)
	}()
	// Wait for verifier entry deterministically
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("verifier did not enter")
	}
	// While blocked, A-side must not be finished and no terminal should have been observed
	if rs.terminal.finished() {
		t.Fatal("A-side terminal must not be finished while guard decision pending")
	}
	close(block)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Recv did not return after unblock")
	}
	if err != nil {
		t.Fatalf("Recv err=%v", err)
	}
	if ev.Kind != lipapi.EventResponseFinished {
		t.Fatalf("ev kind=%q want response_finished (controlled fallback)", ev.Kind)
	}
	// Must not leak raw backend terminal; must be controlled fallback
	if ev.FinishReason == "raw_backend_finish" {
		t.Fatalf("raw backend terminal leaked to A-side; must be suppressed, got FinishReason=%q", ev.FinishReason)
	}
	if ev.FinishReason != guardContinuationPendingReason {
		t.Fatalf("expected controlled fallback FinishReason=%q, got %q", guardContinuationPendingReason, ev.FinishReason)
	}
	// For interim 6.2, held still surfaces controlled fallback to avoid hanging, so finished should be true
	if !rs.terminal.finished() {
		t.Fatal("A-side should be finished via controlled fallback after held CONTINUE (interim)")
	}
}

// TestAgentLoopGuard_DisabledParity ensures guard disabled leaves behavior unchanged.
func TestAgentLoopGuard_DisabledParity_Holdback(t *testing.T) {
	t.Parallel()
	_, rs, _ := setupGuardedStreamForHoldback(t, nil, false)
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw"})
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.Kind != lipapi.EventResponseFinished {
		t.Fatalf("kind=%q", ev.Kind)
	}
	if ev.FinishReason != "raw" {
		t.Fatalf("disabled should preserve raw FinishReason, got %q", ev.FinishReason)
	}
	if !rs.terminal.finished() {
		t.Fatal("disabled guard must terminalize")
	}
}

// TestAgentLoopGuard_CancelAuthoritative ensures cancellation is authoritative over in-flight verifier.
func TestAgentLoopGuard_CancelAuthoritative_OverVerifier(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	observedCancel := make(chan struct{}, 1)
	block := make(chan struct{})
	fv := &fakeGuardVerifierWithBlock{
		enteredCh:     entered,
		blockCh:       block,
		verdict:       stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "work"},
		observeCancel: observedCancel,
	}
	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var err error
	go func() {
		_, err = testRecvOne(ctx, rs, lipapi.Event{Kind: lipapi.EventResponseFinished})
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("verifier did not enter")
	}
	cancel()
	select {
	case <-observedCancel:
	case <-time.After(2 * time.Second):
		t.Fatal("verifier did not observe cancellation")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Recv did not return after cancel")
	}
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, io.EOF) {
		t.Logf("cancel Recv err=%v", err)
	}
	if !rs.terminal.finished() {
		t.Fatal("cancellation must terminalize A-side exactly once")
	}
}

// TestAgentLoopGuard_ExactlyOnce_AttemptSettlement verifies replayed finishes cannot re-settle and outcome is truthful.
func TestAgentLoopGuard_ExactlyOnce_AttemptSettlement(t *testing.T) {
	t.Parallel()
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "remaining work", Reason: "pending"}}
	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	var loggedCount atomic.Int64
	var lastOutcome atomic.Value // stores lipapi.AttemptOutcome
	attempt := rs.attempt.snapshot()
	if attempt == nil {
		t.Fatal("attempt nil")
	}
	origFn := attempt.recordAttemptLoggedFn
	attempt.recordAttemptLoggedFn = func(ctx context.Context, p recordAttemptParams, attrs diag.AttrOpts) {
		loggedCount.Add(1)
		lastOutcome.Store(p.Outcome)
		if origFn != nil {
			origFn(ctx, p, attrs)
		}
	}
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished})
	if err != nil {
		t.Fatalf("first Recv: %v", err)
	}
	if ev.FinishReason != guardContinuationPendingReason {
		t.Fatalf("held CONTINUE must surface controlled fallback, got FinishReason=%q", ev.FinishReason)
	}
	if got := loggedCount.Load(); got != 1 {
		t.Fatalf("after first finish, logged=%d want 1", got)
	}
	if out, ok := lastOutcome.Load().(lipapi.AttemptOutcome); !ok || out != lipapi.AttemptSwallowedFailure {
		t.Fatalf("held B-attempt must be swallowed, got %v", lastOutcome.Load())
	}
	// Replay same attempt via direct record should not re-settle (terminal CAS)
	attempt.recordAttemptLogged(context.Background(), recordAttemptParams{ALegID: rs.facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSuccess}, diag.AttrOpts{})
	if got := loggedCount.Load(); got != 1 {
		t.Fatalf("replay must not re-settle: logged=%d want 1", got)
	}
	if out, ok := lastOutcome.Load().(lipapi.AttemptOutcome); !ok || out != lipapi.AttemptSwallowedFailure {
		t.Fatalf("outcome must remain swallowed after replay, got %v", lastOutcome.Load())
	}
}

// TestAgentLoopGuard_AllowedStop_RecordsSuccess verifies allowed stop persists success.
func TestAgentLoopGuard_AllowedStop_RecordsSuccess(t *testing.T) {
	t.Parallel()
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "complete"}}
	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	var loggedCount atomic.Int64
	var lastOutcome atomic.Value
	attempt := rs.attempt.snapshot()
	origFn := attempt.recordAttemptLoggedFn
	attempt.recordAttemptLoggedFn = func(ctx context.Context, p recordAttemptParams, attrs diag.AttrOpts) {
		loggedCount.Add(1)
		lastOutcome.Store(p.Outcome)
		if origFn != nil {
			origFn(ctx, p, attrs)
		}
	}
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw"})
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.FinishReason != "raw" {
		t.Fatalf("allowed stop must preserve raw terminal, got %q", ev.FinishReason)
	}
	if got := loggedCount.Load(); got != 1 {
		t.Fatalf("logged=%d want 1", got)
	}
	if out, ok := lastOutcome.Load().(lipapi.AttemptOutcome); !ok || out != lipapi.AttemptSuccess {
		t.Fatalf("allowed B-attempt must be success, got %v", lastOutcome.Load())
	}
	if !rs.terminal.finished() {
		t.Fatal("allowed stop must terminalize A-side")
	}
}

// helpers

type fakeGuardVerifierWithBlock struct {
	enteredCh     chan struct{}
	blockCh       chan struct{}
	verdict       stopguard.Verdict
	err           error
	observeCancel chan struct{}
}

func (f *fakeGuardVerifierWithBlock) Verify(ctx context.Context, _ stopguard.Evidence) (stopguard.Verdict, error) {
	if f.enteredCh != nil {
		select {
		case f.enteredCh <- struct{}{}:
		default:
		}
	}
	select {
	case <-ctx.Done():
		if f.observeCancel != nil {
			select {
			case f.observeCancel <- struct{}{}:
			default:
			}
		}
		return stopguard.Verdict{Kind: stopguard.VerdictUncertain}, ctx.Err()
	case <-f.blockCh:
	}
	if f.err != nil {
		return stopguard.Verdict{Kind: stopguard.VerdictUncertain}, f.err
	}
	return f.verdict, nil
}

func setupGuardedStreamForHoldback(t *testing.T, verifier stopguard.Verifier, guardEnabled bool) (*Executor, *retryRecvStream, *fakeGuardVerifier) {
	t.Helper()
	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex.Store = store
	var fv *fakeGuardVerifier
	if guardEnabled {
		if verifier == nil {
			fv = &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop}}
			verifier = fv
		} else if v, ok := verifier.(*fakeGuardVerifier); ok {
			fv = v
		}
		ex.LoopGuard = newLoopGuardForTest(verifier)
	}
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{
				ID:       "holdback-test",
				Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
				Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
			},
			aLegID:  "a-hold-1",
			traceID: "trace-hold-1",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-hold-1", Seq: 1}, routing.AttemptCandidate{
			Key:     "openai:gpt-4",
			Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
		}, authorityLifecycle{}),
		responsePipeline: &responsePipeline{},
	}
	bindTestRuntimeOwners(rs, ex)
	return ex, rs, fv
}
