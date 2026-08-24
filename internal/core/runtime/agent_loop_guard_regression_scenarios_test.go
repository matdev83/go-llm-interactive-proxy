package runtime

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuationsafety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopgate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario 1: "Let me run the tests next." + clean stop + no test action ->
// verifier CONTINUE, no A terminal, continuation executes.
// (Requirements 5.1, 5.3, 6.3, 7.5, 12.1, Design Testing Strategy Scenario 1)
func TestAgentLoopGuard_IntegrationScenario1_ImmediatePromisedAction_Continues(t *testing.T) {
	t.Parallel()

	var verifyCount int
	fv := &fakeGuardVerifierWithCount{
		fn: func() (stopguard.Verdict, error) {
			verifyCount++
			if verifyCount == 1 {
				return stopguard.Verdict{
					Kind:               stopguard.VerdictContinue,
					Reason:             "immediate promised test run not yet executed",
					RemainingObjective: "run the test suite to verify math.go",
				}, nil
			}
			return stopguard.Verdict{
				Kind:   stopguard.VerdictAllowStop,
				Reason: "tests completed",
			}, nil
		},
	}

	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)

	b2Events := []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "Running go test ./... -> PASS\nAll tests passed."},
		{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
	}
	execSetupGuardContinuationOpener(t, rs, b2Events)

	// B1 reaches clean stop with promised action
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{
		Kind:         lipapi.EventResponseFinished,
		FinishReason: "stop",
	})
	require.NoError(t, err)

	// Intermediate B1 terminal was held back; B2 continuation text received
	assert.Equal(t, lipapi.EventTextDelta, ev.Kind)
	assert.Contains(t, ev.Delta, "Running go test")

	// Next event is final single terminal
	ev2, err := rs.Recv(context.Background())
	require.NoError(t, err)
	assert.Equal(t, lipapi.EventResponseFinished, ev2.Kind)

	// Exactly one final terminal; subsequent Recv is EOF
	_, err = rs.Recv(context.Background())
	assert.ErrorIs(t, err, io.EOF)
	assert.True(t, rs.terminal.finished())
	assert.Equal(t, 2, verifyCount)
}

// Scenario 2: "Done; tests pass." + clean stop -> ALLOW_STOP, one A terminal.
// (Requirements 5.2, 7.1, 12.2, Design Testing Strategy Scenario 2)
func TestAgentLoopGuard_IntegrationScenario2_DoneTestsPass_AllowStop(t *testing.T) {
	t.Parallel()

	var verifyCount int
	fv := &fakeGuardVerifierWithCount{
		fn: func() (stopguard.Verdict, error) {
			verifyCount++
			return stopguard.Verdict{
				Kind:   stopguard.VerdictAllowStop,
				Reason: "work complete and tests verified",
			}, nil
		},
	}

	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)

	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{
		Kind:         lipapi.EventResponseFinished,
		FinishReason: "stop",
	})
	require.NoError(t, err)

	assert.Equal(t, lipapi.EventResponseFinished, ev.Kind)
	assert.True(t, rs.terminal.finished())
	assert.Equal(t, 1, verifyCount)

	_, err = rs.Recv(context.Background())
	assert.ErrorIs(t, err, io.EOF)
}

// Scenario 3: complete summary + optional "Next steps" assigned to user -> no continuation.
// (Requirements 5.2, 6.6, 7.4, 12.4, Design Testing Strategy Scenario 3)
func TestAgentLoopGuard_IntegrationScenario3_SummaryWithUserNextSteps_NoContinuation(t *testing.T) {
	t.Parallel()

	var verifyCount int
	fv := &fakeGuardVerifierWithCount{
		fn: func() (stopguard.Verdict, error) {
			verifyCount++
			return stopguard.Verdict{
				Kind:   stopguard.VerdictAllowStop,
				Reason: "implementation finished; next steps are assigned to user/operator",
			}, nil
		},
	}

	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)

	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{
		Kind:         lipapi.EventResponseFinished,
		FinishReason: "stop",
	})
	require.NoError(t, err)

	assert.Equal(t, lipapi.EventResponseFinished, ev.Kind)
	assert.True(t, rs.terminal.finished())
	assert.Equal(t, 1, verifyCount)

	_, err = rs.Recv(context.Background())
	assert.ErrorIs(t, err, io.EOF)
}

// Scenario 4: "Would you like me to do X?" -> NEEDS_USER, no synthesized approval.
// (Requirements 5.4, 6.4, 7.3, 12.3, Design Testing Strategy Scenario 4)
func TestAgentLoopGuard_IntegrationScenario4_DirectUserQuestion_NeedsUserNoSynthApproval(t *testing.T) {
	t.Parallel()

	var verifyCount int
	fv := &fakeGuardVerifierWithCount{
		fn: func() (stopguard.Verdict, error) {
			verifyCount++
			return stopguard.Verdict{
				Kind:   stopguard.VerdictNeedsUser,
				Reason: "assistant asked the user a clarifying question",
			}, nil
		},
	}

	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)

	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{
		Kind:         lipapi.EventResponseFinished,
		FinishReason: "stop",
	})
	require.NoError(t, err)

	// Released to A-side so user can respond; no autonomous continuation opened
	assert.Equal(t, lipapi.EventResponseFinished, ev.Kind)
	assert.True(t, rs.terminal.finished())
	assert.Equal(t, 1, verifyCount)

	_, err = rs.Recv(context.Background())
	assert.ErrorIs(t, err, io.EOF)
}

// Scenario 5: complete answer + "I can also..." -> no continuation.
// (Requirements 5.2, 6.6, 7.2, 12.4, Design Testing Strategy Scenario 5)
func TestAgentLoopGuard_IntegrationScenario5_CompleteAnswerWithICanAlso_NoContinuation(t *testing.T) {
	t.Parallel()

	var verifyCount int
	fv := &fakeGuardVerifierWithCount{
		fn: func() (stopguard.Verdict, error) {
			verifyCount++
			return stopguard.Verdict{
				Kind:   stopguard.VerdictAllowStop,
				Reason: "original request fulfilled; 'I can also' is an optional offer",
			}, nil
		},
	}

	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)

	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{
		Kind:         lipapi.EventResponseFinished,
		FinishReason: "stop",
	})
	require.NoError(t, err)

	assert.Equal(t, lipapi.EventResponseFinished, ev.Kind)
	assert.True(t, rs.terminal.finished())
	assert.Equal(t, 1, verifyCount)

	_, err = rs.Recv(context.Background())
	assert.ErrorIs(t, err, io.EOF)
}

// Scenario 6: quoted "I'll continue" -> quotation alone does not continue.
// (Requirements 5.2, 7.6, Design Testing Strategy Scenario 6)
func TestAgentLoopGuard_IntegrationScenario6_QuotedIllContinue_NoContinuation(t *testing.T) {
	t.Parallel()

	var verifyCount int
	fv := &fakeGuardVerifierWithCount{
		fn: func() (stopguard.Verdict, error) {
			verifyCount++
			return stopguard.Verdict{
				Kind:   stopguard.VerdictAllowStop,
				Reason: "review completed; quoted text does not express assistant commitment",
			}, nil
		},
	}

	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)

	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{
		Kind:         lipapi.EventResponseFinished,
		FinishReason: "stop",
	})
	require.NoError(t, err)

	assert.Equal(t, lipapi.EventResponseFinished, ev.Kind)
	assert.True(t, rs.terminal.finished())
	assert.Equal(t, 1, verifyCount)

	_, err = rs.Recv(context.Background())
	assert.ErrorIs(t, err, io.EOF)
}

// Scenario 7: pre-output EOF -> existing recovery, no intermediate A terminal.
// (Requirements 3.1, 3.2, 12.5, Design Testing Strategy Scenario 7)
func TestAgentLoopGuard_IntegrationScenario7_PreOutputEOF_ExistingRecoveryNoIntermediateATerminal(t *testing.T) {
	t.Parallel()

	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(42)
	ex.StreamRecovery = streamrecovery.Config{
		Enabled:                     true,
		IdleTimeout:                 5 * time.Millisecond,
		GracePeriod:                 0,
		AllowPostOutputContinuation: true,
	}

	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop}}
	ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"primary": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				// B1 fails immediately before emitting any output
				return lipapi.NewFixedEventStream([]lipapi.Event{}), nil
			},
		},
		"fallback": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				// B2 recovers pre-output
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "recovered response"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "primary:gpt-4|fallback:gpt-4"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("task")}}},
	}

	stream, err := ex.Execute(context.Background(), call)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	events, err := collectAll(stream)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("collect: %v", err)
	}

	assert.Equal(t, int32(2), opens.Load(), "pre-output recovery must open B2")
	assert.Equal(t, 1, countTerminal(events), "must have exactly one final EventResponseFinished")

	var gotText string
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta {
			gotText += ev.Delta
		}
	}
	assert.Equal(t, "recovered response", gotText)
}

// Scenario 8: post-output EOF after text -> no replay/duplicate; safe continuation when supported.
// (Requirements 4.1, 4.2, 4.3, 12.6, Design Testing Strategy Scenario 8)
func TestAgentLoopGuard_IntegrationScenario8_PostOutputEOF_NoReplaySafeContinuation(t *testing.T) {
	t.Parallel()

	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(100)
	ex.StreamRecovery = streamrecovery.Config{
		Enabled:                     true,
		IdleTimeout:                 5 * time.Millisecond,
		GracePeriod:                 0,
		AllowPostOutputContinuation: true,
	}

	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop}}
	ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				n := opens.Add(1)
				if n == 1 {
					// B1 emits text "hello" then drops with EOF
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventTextDelta, Delta: "hello"},
					}), nil
				}
				// B2 continues without replaying "hello"
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: " world"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("task")}}},
	}

	stream, err := ex.Execute(context.Background(), call)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	events, err := collectAll(stream)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("collect: %v", err)
	}

	assert.Equal(t, int32(2), opens.Load())
	assert.Equal(t, 1, countTerminal(events))

	var fullText string
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta {
			fullText += ev.Delta
		}
	}
	assert.Equal(t, "hello world", fullText, "must stitch continuous text without duplicating 'hello'")
}

// Scenario 9: post-output EOF after completed tool+matching result -> continue without tool re-execution.
// (Requirements 4.4, 12.6, Design Testing Strategy Scenario 9)
func TestAgentLoopGuard_IntegrationScenario9_PostOutputEOF_RetainedToolResultNoReexecution(t *testing.T) {
	t.Parallel()

	var verifyCount int
	fv := &fakeGuardVerifierWithCount{
		fn: func() (stopguard.Verdict, error) {
			verifyCount++
			if verifyCount == 1 {
				return stopguard.Verdict{
					Kind:               stopguard.VerdictContinue,
					RemainingObjective: "summarize tool result",
					Reason:             "tool finished, summary pending",
				}, nil
			}
			return stopguard.Verdict{Kind: stopguard.VerdictAllowStop}, nil
		},
	}

	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)

	// B2 continuation provides text summary of retained tool result
	b2Events := []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "Tool completed successfully with status 200 OK."},
		{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
	}
	execSetupGuardContinuationOpener(t, rs, b2Events)

	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{
		Kind:         lipapi.EventResponseFinished,
		FinishReason: "stop",
	})
	require.NoError(t, err)

	assert.Equal(t, lipapi.EventTextDelta, ev.Kind)
	assert.Contains(t, ev.Delta, "Tool completed successfully")

	ev2, err := rs.Recv(context.Background())
	require.NoError(t, err)
	assert.Equal(t, lipapi.EventResponseFinished, ev2.Kind)

	_, err = rs.Recv(context.Background())
	assert.ErrorIs(t, err, io.EOF)
}

// Scenario 10: EOF mid-tool args -> no guessed execution/replay.
// (Requirements 4.5, 12.7, Design Testing Strategy Scenario 10)
func TestAgentLoopGuard_IntegrationScenario10_EOFMidToolArgs_NoGuessedReplay(t *testing.T) {
	t.Parallel()

	// Direct policy check: partial tool call with unproven resume surfaces failure, never continues blindly
	cand := stopguard.Candidate{
		Cause:            stopguard.CausePartialToolCall,
		OutputCommitted:  true,
		SafeNativeResume: false,
	}
	decision := stopguard.Decide(cand, stopguard.PolicyTrust)
	assert.Equal(t, stopguard.ActionSurfaceFailure, decision.Action,
		"partial tool arguments must surface failure and never replay or guess args")
	assert.False(t, decision.Verify)
}

// Scenario 11: client cancel while verifier active -> cancel wins; no hidden continuation.
// (Requirements 3.5, 5.6, 9.4, 12.8, Design Testing Strategy Scenario 11)
func TestAgentLoopGuard_IntegrationScenario11_ClientCancel_CancelWinsNoContinuation(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{}, 1)
	block := make(chan struct{})
	fv := &fakeGuardVerifierWithBlock{
		enteredCh: entered,
		blockCh:   block,
		verdict:   stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "work"},
	}

	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var ev lipapi.Event
	var recvErr error
	go func() {
		ev, recvErr = testRecvOne(ctx, rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"})
		close(done)
	}()

	// Wait for verifier to become active
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("verifier did not enter")
	}

	// Client cancels during verification
	cancel()
	close(block)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Recv did not return after cancellation")
	}

	// Cancel was authoritative
	assert.True(t, rs.terminal.finished())
	_ = ev
	_ = recvErr
}

// Scenario 12: verifier timeout/error -> held terminal released.
// (Requirements 5.6, 8.2, 12.8, Design Testing Strategy Scenario 12)
func TestAgentLoopGuard_IntegrationScenario12_VerifierTimeoutOrError_HeldTerminalReleased(t *testing.T) {
	t.Parallel()

	fv := &fakeGuardVerifier{
		err: context.DeadlineExceeded,
	}

	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)

	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{
		Kind:         lipapi.EventResponseFinished,
		FinishReason: "raw_stop",
	})
	require.NoError(t, err)

	// Fails safe toward stopping: held terminal released, single terminal published
	assert.Equal(t, lipapi.EventResponseFinished, ev.Kind)
	assert.True(t, rs.terminal.finished())

	_, err = rs.Recv(context.Background())
	assert.ErrorIs(t, err, io.EOF)
}

// Scenario 13: repeated identical final output -> no-progress breaker and exactly one terminal.
// (Requirements 8.3, 8.4, 12.9, Design Testing Strategy Scenario 13)
func TestAgentLoopGuard_IntegrationScenario13_RepeatedIdenticalOutput_NoProgressBreaker(t *testing.T) {
	t.Parallel()

	// Given noProgressLimit = 2
	gate := stopgate.New(stopgate.Ports{
		Verifier: &fakeGuardVerifier{
			verdict: stopguard.Verdict{
				Kind:               stopguard.VerdictContinue,
				Reason:             "stuck",
				RemainingObjective: "same objective",
			},
		},
		Now: time.Now,
	}, stopgate.Config{
		Enabled:                  true,
		ExplicitCompletionPolicy: stopguard.PolicyTrust,
		MaxSemanticContinuations: 5,
		NoProgressLimit:          2,
	})

	tf := stopgate.TerminalFacts{
		Candidate: stopguard.Candidate{
			Cause:           stopguard.CauseNormalEnd,
			OutputCommitted: true,
		},
		SupportsContinuation: true,
	}

	// First continuation: establishes baseline, opens leg 1
	o1 := gate.ObserveCandidate(context.Background(), tf)
	assert.Equal(t, stopguard.ActionContinueLeg, o1.Action)

	// Second continuation with identical facts (repeat 1): within limit
	o2 := gate.ObserveCandidate(context.Background(), tf)
	assert.Equal(t, stopguard.ActionContinueLeg, o2.Action)

	// Third continuation with identical facts (repeat 2 = limit reached): breaker trips
	o3 := gate.ObserveCandidate(context.Background(), tf)
	assert.Equal(t, stopguard.ActionForwardTerminal, o3.Action, "no-progress breaker must trip and release terminal")
	assert.Contains(t, o3.Reason, "no_progress")

	// Subsequent candidates remain latched
	o4 := gate.ObserveCandidate(context.Background(), tf)
	assert.Equal(t, stopguard.ActionForwardTerminal, o4.Action)
}

// Scenario 14: maximum semantic continuation budget -> exactly one terminal/error.
// (Requirements 8.1, 8.4, 12.9, Design Testing Strategy Scenario 14)
func TestAgentLoopGuard_IntegrationScenario14_MaxSemanticContinuationsBudget_SingleTerminal(t *testing.T) {
	t.Parallel()

	// Given maxContinuations = 3
	gate := stopgate.New(stopgate.Ports{
		Verifier: &fakeGuardVerifier{
			verdict: stopguard.Verdict{
				Kind:               stopguard.VerdictContinue,
				Reason:             "more work",
				RemainingObjective: "next step",
			},
		},
		Now: time.Now,
	}, stopgate.Config{
		Enabled:                  true,
		ExplicitCompletionPolicy: stopguard.PolicyTrust,
		MaxSemanticContinuations: 3,
		NoProgressLimit:          5,
	})

	// Leg 1
	tf1 := stopgate.TerminalFacts{
		Candidate:            stopguard.Candidate{Cause: stopguard.CauseNormalEnd, OutputCommitted: true},
		SupportsContinuation: true,
		Tail: continuationsafety.TailState{
			CommittedAssistantItems: []lipapi.Item{{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleAssistant, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "part 1"}}}},
		},
	}
	assert.Equal(t, stopguard.ActionContinueLeg, gate.ObserveCandidate(context.Background(), tf1).Action)

	// Leg 2
	tf2 := stopgate.TerminalFacts{
		Candidate:            stopguard.Candidate{Cause: stopguard.CauseNormalEnd, OutputCommitted: true},
		SupportsContinuation: true,
		Tail: continuationsafety.TailState{
			CommittedAssistantItems: []lipapi.Item{{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleAssistant, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "part 2"}}}},
		},
	}
	assert.Equal(t, stopguard.ActionContinueLeg, gate.ObserveCandidate(context.Background(), tf2).Action)

	// Leg 3
	tf3 := stopgate.TerminalFacts{
		Candidate:            stopguard.Candidate{Cause: stopguard.CauseNormalEnd, OutputCommitted: true},
		SupportsContinuation: true,
		Tail: continuationsafety.TailState{
			CommittedAssistantItems: []lipapi.Item{{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleAssistant, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "part 3"}}}},
		},
	}
	assert.Equal(t, stopguard.ActionContinueLeg, gate.ObserveCandidate(context.Background(), tf3).Action)

	// 4th continuation attempt exceeds max budget of 3 -> forward terminal
	tf4 := stopgate.TerminalFacts{
		Candidate:            stopguard.Candidate{Cause: stopguard.CauseNormalEnd, OutputCommitted: true},
		SupportsContinuation: true,
		Tail: continuationsafety.TailState{
			CommittedAssistantItems: []lipapi.Item{{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleAssistant, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "part 4"}}}},
		},
	}
	o4 := gate.ObserveCandidate(context.Background(), tf4)
	assert.Equal(t, stopguard.ActionForwardTerminal, o4.Action, "exceeding max semantic continuations must forward terminal")
	assert.Contains(t, o4.Reason, "budget")
}

// Scenario 15: trusted explicit completion -> semantic verifier skipped/relaxed per policy.
// (Requirements 5.7, 7.1, 10.3, Design Testing Strategy Scenario 15)
func TestAgentLoopGuard_IntegrationScenario15_TrustedExplicitCompletion_VerifierBypassed(t *testing.T) {
	t.Parallel()

	fv := &fakeGuardVerifier{
		verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "unwanted"},
	}

	gate := stopgate.New(stopgate.Ports{
		Verifier: fv,
		Now:      time.Now,
	}, stopgate.Config{
		Enabled:                  true,
		ExplicitCompletionPolicy: stopguard.PolicyTrust,
		MaxSemanticContinuations: 3,
		NoProgressLimit:          2,
	})

	tf := stopgate.TerminalFacts{
		Candidate: stopguard.Candidate{
			Cause:              stopguard.CauseNormalEnd,
			OutputCommitted:    true,
			ExplicitCompletion: true,
		},
		SupportsContinuation: true,
	}

	outcome := gate.ObserveCandidate(context.Background(), tf)
	assert.Equal(t, stopguard.ActionForwardTerminal, outcome.Action)
	assert.Equal(t, 0, fv.CallCount(), "trusted explicit completion must bypass verifier completely")
}

// Scenario 16: unsupported A-side continuation capability -> clean final fallback, no malformed stream.
// (Requirements 1.5, 10.4, 12.10, Design Testing Strategy Scenario 16)
func TestAgentLoopGuard_IntegrationScenario16_UnsupportedContinuationCapability_CleanFallback(t *testing.T) {
	t.Parallel()

	fv := &fakeGuardVerifier{
		verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "more work"},
	}

	gate := stopgate.New(stopgate.Ports{
		Verifier: fv,
		Now:      time.Now,
	}, stopgate.Config{
		Enabled:                  true,
		ExplicitCompletionPolicy: stopguard.PolicyTrust,
		MaxSemanticContinuations: 3,
		NoProgressLimit:          2,
	})

	tf := stopgate.TerminalFacts{
		Candidate: stopguard.Candidate{
			Cause:           stopguard.CauseNormalEnd,
			OutputCommitted: true,
		},
		SupportsContinuation: false, // Unsupported continuation capability
	}

	outcome := gate.ObserveCandidate(context.Background(), tf)
	assert.Equal(t, stopguard.ActionForwardTerminal, outcome.Action,
		"unsupported continuation capability must fall back cleanly to final terminal")
	assert.Contains(t, outcome.Reason, "unsupported")
}
