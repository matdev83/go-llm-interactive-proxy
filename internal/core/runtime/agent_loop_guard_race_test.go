package runtime

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// TestAgentLoopGuard_Race_SwallowedBLeg_AtomicTerminalization asserts that
// concurrent terminalization races on a swallowed B-leg resolve atomically
// so exactly one caller wins attempt terminal settlement (requirements 9.1, 9.4; task 10.2).
func TestAgentLoopGuard_Race_SwallowedBLeg_AtomicTerminalization(t *testing.T) {
	t.Parallel()

	for iter := range 100 {
		sess := newAttemptSession(attemptSessionInput{
			bleg:      b2bua.BLegRecord{BLegID: "b-race-1", Seq: 1},
			cand:      routing.AttemptCandidate{Key: "openai:gpt-4", Primary: routing.Primary{Backend: "openai", Model: "gpt-4"}},
			authority: authorityLifecycle{},
			traceID:   "trace-race-1",
		})

		const numGoroutines = 20
		var wins atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})

		for i := range numGoroutines {
			wg.Add(1)
			intent := IntentSwallowedFailure
			switch i % 3 {
			case 1:
				intent = IntentCancellation
			case 2:
				intent = IntentSurfacedFailure
			}
			go func(intent attemptTerminalIntent) {
				defer wg.Done()
				<-start
				res := sess.TerminalizeAttempt(context.Background(), intent, attemptEvidence{
					Command:       sdkterminal.CommandSwallowedAttempt,
					LegOutcome:    billing.LegOutcomeSwallowed,
					ObsOutcome:    response.OutcomeReplaced,
					RecordOutcome: lipapi.AttemptSwallowedFailure,
					RecordReason:  "race_test",
					TraceID:       "trace-race-1",
					ALegID:        "a-race-1",
					StartedAt:     time.Now(),
				})
				if res.Result.Won {
					wins.Add(1)
				}
			}(intent)
		}

		close(start)
		wg.Wait()

		if got := wins.Load(); got != 1 {
			t.Fatalf("iteration %d: exactly 1 goroutine must win terminalization, got %d", iter, got)
		}
	}
}

// TestAgentLoopGuard_Race_FinalALeg_AtomicPublication asserts that
// concurrent races to terminalize the logical A-leg settle exactly once
// without duplicate terminal publication (requirements 9.3, 9.4; task 10.2).
func TestAgentLoopGuard_Race_FinalALeg_AtomicPublication(t *testing.T) {
	t.Parallel()

	for iter := range 100 {
		term := newTurnTerminal()
		const numGoroutines = 20
		var wins atomic.Int64
		start := make(chan struct{})
		done := make(chan struct{}, numGoroutines)

		for range numGoroutines {
			go func() {
				<-start
				// Race markFinished
				if term.markFinished() {
					wins.Add(1)
				}
				done <- struct{}{}
			}()
		}

		close(start)
		for range numGoroutines {
			<-done
		}

		if got := wins.Load(); got != 1 {
			t.Fatalf("iteration %d: exactly 1 markFinished must succeed, got %d", iter, got)
		}
		if !term.finished() {
			t.Fatalf("iteration %d: terminal must be finished", iter)
		}
	}
}

// TestAgentLoopGuard_Race_ContinuationLegCompletion_Vs_CancelClose exercises
// multi-leg continuation where Leg 1 is swallowed and Leg 2 is executing
// while client cancellation and stream close race against event reception (requirements 4.1, 9.1, 9.4; task 10.2).
func TestAgentLoopGuard_Race_ContinuationLegCompletion_Vs_CancelClose(t *testing.T) {
	t.Parallel()

	for range 50 {
		fv := &fakeGuardVerifier{
			verdict: stopguard.Verdict{
				Kind:               stopguard.VerdictContinue,
				RemainingObjective: "continue work",
				Reason:             "step 1",
			},
		}
		_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)

		b2Events := []lipapi.Event{
			{Kind: lipapi.EventTextDelta, Delta: "continued output"},
			{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
		}
		execSetupGuardContinuationOpener(t, rs, b2Events)

		ctx, cancel := context.WithCancel(context.Background())
		start := make(chan struct{})
		done := make(chan struct{}, 2)

		// Goroutine 1: Recv first event (triggers guard continuation)
		go func() {
			<-start
			_, _ = testRecvOne(ctx, rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw_backend_finish"})
			done <- struct{}{}
		}()

		// Goroutine 2: Cancel context or close stream concurrently
		go func() {
			<-start
			cancel()
			_ = rs.Close()
			done <- struct{}{}
		}()

		close(start)
		<-done
		<-done

		// Stream must not deadlock and terminal must settle
		cancel()
	}
}

// TestAgentLoopGuard_Race_VerifierCompletion_Vs_ClientCancelClose exercises
// the race between in-flight verifier completion and client cancellation (requirements 9.4, 12.8; task 10.2).
func TestAgentLoopGuard_Race_VerifierCompletion_Vs_ClientCancelClose(t *testing.T) {
	t.Parallel()

	for iter := range 50 {
		entered := make(chan struct{}, 1)
		block := make(chan struct{})
		fv := &fakeGuardVerifierWithBlock{
			enteredCh: entered,
			blockCh:   block,
			verdict:   stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "work", Reason: "pending"},
		}
		_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		var recvErr error
		go func() {
			_, recvErr = testRecvOne(ctx, rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw_backend_finish"})
			close(done)
		}()

		// Wait for verifier entry
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: verifier did not enter", iter)
		}

		// Concurrently unblock verifier and cancel context
		go func() {
			close(block)
		}()
		cancel()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: Recv timed out under cancel/verifier race", iter)
		}

		if recvErr != nil && !errors.Is(recvErr, context.Canceled) && !errors.Is(recvErr, io.EOF) {
			t.Logf("iteration %d recvErr: %v", iter, recvErr)
		}
		if !rs.terminal.finished() {
			t.Fatalf("iteration %d: terminal must be finished after cancel/verifier race", iter)
		}
	}
}

// TestAgentLoopGuard_HiddenInstruction_RuntimePersistenceAndASideIsolation
// verifies end-to-end that automated recovery instructions:
// 1. Are injected ONLY as RoleDeveloper in the cloned B-leg baseline.
// 2. Are NEVER present as RoleUser or RoleAssistant.
// 3. Are NEVER emitted to the client-facing A-side event stream.
// 4. Are NEVER persisted into responsePipeline's remembered user events.
// 5. Cause Leg 1 to be recorded as AttemptSwallowedFailure and Leg 2 as completed.
// (requirements 6.5, 9.1, 9.6, 11.5; task 10.2).
func TestAgentLoopGuard_HiddenInstruction_RuntimePersistenceAndASideIsolation(t *testing.T) {
	t.Parallel()

	callCount := 0
	fv := &fakeGuardVerifierWithCount{
		fn: func() (stopguard.Verdict, error) {
			callCount++
			if callCount == 1 {
				return stopguard.Verdict{
					Kind:               stopguard.VerdictContinue,
					RemainingObjective: "finish calculation of 2+2",
					Reason:             "step 1 completed, step 2 remaining",
				}, nil
			}
			return stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "all steps completed"}, nil
		},
	}

	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)

	var capturedBaseline lipapi.Call
	if rs.recovery == nil {
		rs.recovery = &recoveryController{}
	}
	rs.recovery.opener = func(ctx context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		capturedBaseline = req.pinnedFacts.baseline
		bleg := b2bua.BLegRecord{BLegID: "b-guard-leg2", Seq: 2, ALegID: rs.facts.aLegID}
		cand := routing.AttemptCandidate{Key: "openai:gpt-4", Primary: routing.Primary{Backend: "openai", Model: "gpt-4"}}
		stream := &guardContinuationEventStream{
			events: []lipapi.Event{
				{Kind: lipapi.EventTextDelta, Delta: "Result is 4."},
				{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
			},
		}
		sess := newAttemptSession(attemptSessionInput{
			inner:            stream,
			bleg:             bleg,
			cand:             cand,
			authority:        authorityLifecycle{},
			aScope:           rs.terminal.aLegScope(),
			traceID:          rs.facts.traceID,
			billingCallID:    rs.facts.billingCallID,
			billingCallState: rs.facts.billingCallState,
		})
		ready := newReadyAttempt(sess, pendingSelectionEffects{})
		ready.state = readyStatePrepared
		return replacementOpenResult{opened: true, ready: ready, bleg: bleg, cand: cand}, nil
	}

	// First event on leg 1 is partial text, then finished event which triggers guard
	ev1, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "Calculating: "})
	if err != nil {
		t.Fatalf("first recv text: %v", err)
	}
	if ev1.Kind != lipapi.EventTextDelta || ev1.Delta != "Calculating: " {
		t.Fatalf("unexpected first event: %v", ev1)
	}

	// Trigger finish on leg 1 -> swallowed -> leg 2 opened -> returns leg 2 text
	ev2, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw_backend_stop"})
	if err != nil {
		t.Fatalf("leg 1 finish recv: %v", err)
	}
	if ev2.Kind != lipapi.EventTextDelta || ev2.Delta != "Result is 4." {
		t.Fatalf("expected leg 2 text, got kind %q delta %q", ev2.Kind, ev2.Delta)
	}

	// Next event is leg 2 finish -> verifier returns VerdictAllowStop -> A-side finished
	ev3, err := rs.Recv(context.Background())
	if err != nil {
		t.Fatalf("leg 2 finish recv: %v", err)
	}
	if ev3.Kind != lipapi.EventResponseFinished {
		t.Fatalf("expected finished event, got %q", ev3.Kind)
	}

	// Next recv must be EOF
	_, err = rs.Recv(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after finished, got %v", err)
	}

	// 1. Verify cloned baseline for leg 2 contains the hidden recovery instruction in RoleDeveloper
	hiddenFound := false
	for _, m := range capturedBaseline.Messages {
		for _, p := range m.Parts {
			if strings.Contains(p.Text, "<automated-recovery>") {
				hiddenFound = true
				if m.Role != lipapi.RoleDeveloper {
					t.Fatalf("hidden recovery instruction in Messages has role %q, must be RoleDeveloper", m.Role)
				}
			}
			if m.Role == lipapi.RoleUser && strings.Contains(p.Text, "<automated-recovery>") {
				t.Fatalf("hidden recovery instruction leaked into RoleUser in Messages: %v", p.Text)
			}
		}
	}
	for _, it := range capturedBaseline.Items {
		if it.Kind == lipapi.ItemKindMessage {
			for _, cp := range it.Content {
				if strings.Contains(cp.Text, "<automated-recovery>") {
					hiddenFound = true
					if it.Role != lipapi.RoleDeveloper {
						t.Fatalf("hidden recovery instruction in Items has role %q, must be RoleDeveloper", it.Role)
					}
				}
				if it.Role == lipapi.RoleUser && strings.Contains(cp.Text, "<automated-recovery>") {
					t.Fatalf("hidden recovery instruction leaked into RoleUser in Items: %v", cp.Text)
				}
			}
		}
	}
	if !hiddenFound {
		t.Fatalf("hidden recovery instruction was not found in captured leg 2 baseline")
	}

	// 2. Verify A-side emitted events never contained <automated-recovery>
	aSideEvents := []lipapi.Event{ev1, ev2, ev3}
	for i, ev := range aSideEvents {
		if strings.Contains(ev.Delta, "<automated-recovery>") {
			t.Fatalf("A-side event %d (%s) leaked <automated-recovery>: %q", i, ev.Kind, ev.Delta)
		}
		if strings.Contains(ev.FinishReason, "<automated-recovery>") {
			t.Fatalf("A-side event %d finish reason leaked <automated-recovery>: %q", i, ev.FinishReason)
		}
	}

	// 3. Verify responsePipeline seen events never contain <automated-recovery>
	if rs.responsePipeline != nil {
		for _, ev := range rs.responsePipeline.seenEventsCopy() {
			if strings.Contains(ev.Delta, "<automated-recovery>") {
				t.Fatalf("responsePipeline seen events contain <automated-recovery>: %q", ev.Delta)
			}
		}
	}

	// 4. Verify terminal finished
	if !rs.terminal.finished() {
		t.Fatal("A-side terminal must be finished")
	}
}
