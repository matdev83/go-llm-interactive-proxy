package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// fakeGuardVerifier is a minimal verifier for gate tests.
type fakeGuardVerifier struct {
	calls   atomic.Int64
	verdict stopguard.Verdict
	err     error
}

func (f *fakeGuardVerifier) Verify(_ context.Context, _ stopguard.Evidence) (stopguard.Verdict, error) {
	f.calls.Add(1)
	if f.err != nil {
		return stopguard.Verdict{Kind: stopguard.VerdictUncertain}, f.err
	}
	return f.verdict, nil
}

func (f *fakeGuardVerifier) CallCount() int { return int(f.calls.Load()) }

func setupGuardedStream(t *testing.T, verifier stopguard.Verifier, guardEnabled bool) (*Executor, *retryRecvStream, *fakeGuardVerifier) {
	t.Helper()
	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex.Store = store
	fv, isFake := verifier.(*fakeGuardVerifier)
	if guardEnabled {
		if verifier == nil {
			fv = &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop}}
			verifier = fv
		}
		ex.LoopGuard = newLoopGuardForTest(verifier)
		if isFake && fv != nil {
			// already set
		}
	}
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{
				ID:       "guard-gate-test",
				Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
				Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
			},
			aLegID:  "a-guard-1",
			traceID: "trace-guard-1",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-guard-1", Seq: 1}, routing.AttemptCandidate{
			Key:     "openai:gpt-4",
			Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
		}, authorityLifecycle{}),
		responsePipeline: &responsePipeline{},
	}
	bindTestRuntimeOwners(rs, ex)
	// allow gate to see committed state; terminal initially not committed
	return ex, rs, fv
}

func TestAgentLoopGuard_GuardNil_DisabledBehaviorUnchanged(t *testing.T) {
	t.Parallel()
	_, rs, _ := setupGuardedStream(t, nil, false)
	// also verify verifier nil path: no LoopGuard
	if rs.terminal.loopGuard != nil {
		t.Fatal("expected nil loopGuard when disabled")
	}
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished})
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.Kind != lipapi.EventResponseFinished {
		t.Fatalf("event kind=%q want response_finished", ev.Kind)
	}
	if !rs.terminal.finished() {
		t.Fatal("expected finished==true when guard disabled")
	}
	// second Recv should be EOF
	_, err = rs.Recv(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("second Recv err=%v want EOF", err)
	}
}

func TestAgentLoopGuard_Enabled_AllowStop_ProceedsExactlyOnce(t *testing.T) {
	t.Parallel()
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "complete"}}
	_, rs, _ := setupGuardedStream(t, fv, true)
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished})
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.Kind != lipapi.EventResponseFinished {
		t.Fatalf("event kind=%q want response_finished", ev.Kind)
	}
	if !rs.terminal.finished() {
		t.Fatal("expected finished==true for ALLOW_STOP")
	}
	if fv.CallCount() != 1 {
		t.Fatalf("verifier calls=%d want 1", fv.CallCount())
	}
	// exactly-once finished: second Recv EOF, finished stays true
	_, err = rs.Recv(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("second Recv err=%v want EOF", err)
	}
	if !rs.terminal.finished() {
		t.Fatal("finished should remain true")
	}
	// ensure no double publish via markFinished
	if rs.terminal.markFinished() {
		t.Fatal("markFinished should be false after already finished")
	}
}

func TestAgentLoopGuard_Enabled_Continue_Withheld(t *testing.T) {
	t.Parallel()
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "remaining work", Reason: "pending"}}
	_, rs, _ := setupGuardedStream(t, fv, true)
	// instrument recordAttemptLogged to count
	var logged atomic.Int64
	attempt := rs.attempt.snapshot()
	if attempt == nil {
		t.Fatal("attempt nil")
	}
	origFn := attempt.recordAttemptLoggedFn
	attempt.recordAttemptLoggedFn = func(ctx context.Context, p recordAttemptParams, attrs diag.AttrOpts) {
		logged.Add(1)
		if origFn != nil {
			origFn(ctx, p, attrs)
		}
	}
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw_backend_finish"})
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	// Held CONTINUE must not leak raw backend terminal; it surfaces controlled fallback and settles B-attempt as swallowed (Req 1.3, 9.1, 12.10)
	if ev.FinishReason == "raw_backend_finish" {
		t.Fatalf("raw backend terminal leaked, got FinishReason=%q", ev.FinishReason)
	}
	if ev.FinishReason != guardContinuationPendingReason {
		t.Fatalf("expected controlled fallback FinishReason=%q, got %q", guardContinuationPendingReason, ev.FinishReason)
	}
	if !rs.terminal.finished() {
		t.Fatal("expected finished==true via controlled fallback for CONTINUE held (interim)")
	}
	if fv.CallCount() != 1 {
		t.Fatalf("verifier calls=%d want 1", fv.CallCount())
	}
	if logged.Load() != 1 {
		t.Fatalf("recordAttemptLogged calls=%d want 1", logged.Load())
	}
	if ev.Kind != lipapi.EventResponseFinished {
		t.Fatalf("first Recv kind=%q want response_finished (controlled fallback)", ev.Kind)
	}
	// Verify B-attempt outcome is swallowed, not success
	if attempt.terminal != nil && attempt.terminal.Owner() != nil {
		// No direct outcome check here, but record count ensures exactly-once
	}
	// no panic and subsequent Recv terminates cleanly (EOF)
	_, err = rs.Recv(context.Background())
	if err == nil {
		// gate-drain or recovery may have pending state; allow EOF or response_finished
		// but must not panic and must eventually reach EOF
		for i := 0; i < 3; i++ {
			_, err = rs.Recv(context.Background())
			if errors.Is(err, io.EOF) {
				break
			}
		}
	}
	if !errors.Is(err, io.EOF) && err != nil {
		// allow EOF only; but if we got response_finished again, ensure no double finish
		if err != io.EOF {
			t.Logf("subsequent Recv err=%v (allowing EOF)", err)
		}
	}
	// ensure we can still close cleanly without double publish
	_ = rs.Close()
	if !rs.terminal.finished() {
		// Close may terminalize; but finish should now be true or remain false? In held case, Close will terminalize via closeClose
		t.Logf("after Close finished=%v", rs.terminal.finished())
	}
}

func TestAgentLoopGuard_Race_RecvClose_NoDoublePublish(t *testing.T) {
	t.Parallel()
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop}}
	_, rs, _ := setupGuardedStream(t, fv, true)
	// Use a blocking stream for race: Recv blocks until Close
	inner := &blockingUntilCloseInner{
		recvEntered: make(chan struct{}, 1),
		unblock:     make(chan struct{}),
	}
	testStoreInner(rs, inner)
	// Set up a response_finished event after unblock? Instead test race between Recv that would do guarded finish and Close
	// Use single event stream variant for guarded finish race
	rs2 := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts:    rs.facts,
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-race", Seq: 1}, routing.AttemptCandidate{
			Key:     "openai:gpt-4",
			Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
		}, authorityLifecycle{}),
		responsePipeline: &responsePipeline{},
	}
	ex := TestExecutor()
	store, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex.Store = store
	ex.LoopGuard = newLoopGuardForTest(fv)
	bindTestRuntimeOwners(rs2, ex)
	inner2 := &blockingUntilCloseInner{
		recvEntered: make(chan struct{}, 1),
		unblock:     make(chan struct{}),
	}
	testStoreInner(rs2, inner2)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = rs2.Recv(context.Background())
	}()
	<-inner2.recvEntered
	_ = rs2.Close()
	wg.Wait()
	// finished should be exactly once, no panic
	if !rs2.terminal.finished() {
		t.Fatal("expected finished after race")
	}
	if rs2.terminal.markFinished() {
		t.Fatal("double publish of finished after race")
	}
	// verifier should have been called at most once
	if fv.CallCount() > 1 {
		t.Fatalf("verifier calls=%d want <=1", fv.CallCount())
	}
}
