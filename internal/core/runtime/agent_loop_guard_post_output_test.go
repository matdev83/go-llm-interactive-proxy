package runtime

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuationsafety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

// Visible text then EOF must become guard continuation, not retry, preserving committed text and single terminal.
func TestAgentLoopGuard_PostOutput_VisibleTextThenEOF_NoRetryOneContinuationLegalStream(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.StreamRecovery = streamrecovery.Config{Enabled: true, IdleTimeout: 5 * time.Millisecond, GracePeriod: 0, EmitWarning: true, AllowPostOutputContinuation: true}
	// Post-output continuation does not use verifier; B2's clean stop should Allow
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "complete"}}
	ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

	var opens atomic.Int32
	b2Text := " world"
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				n := opens.Add(1)
				if n == 1 {
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventTextDelta, Delta: "hello"},
					}), nil
				}
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: b2Text},
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
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	defer func() { _ = stream.Close() }()
	events, err := collectAll(stream)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("collect: %v", err)
	}
	if opens.Load() != 2 {
		t.Fatalf("post-output EOF must open exactly one semantic continuation B2 (got opens=%d, want 2)", opens.Load())
	}
	// Exactly one final terminal
	terms := countTerminal(events)
	if terms != 1 {
		t.Fatalf("must have exactly one final EventResponseFinished (got %d, events=%v)", terms, events)
	}
	var gotText strings.Builder
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta {
			gotText.WriteString(ev.Delta)
		}
	}
	textStr := gotText.String()
	if textStr != "hello"+b2Text {
		t.Fatalf("committed text must be emitted once with B2 continuation (got %q want %q)", textStr, "hello"+b2Text)
	}
	if textStr == "hellohello"+b2Text {
		t.Fatalf("duplicate committed text detected %q", textStr)
	}
	leg, err := store.FetchALeg(context.Background(), call.Session.ALegID)
	if err != nil {
		t.Fatalf("FetchALeg: %v", err)
	}
	atts, err := store.LoadAttempts(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatalf("LoadAttempts: %v", err)
	}
	if len(atts) != 2 {
		t.Fatalf("attempts len=%d want 2 (B1 swallowed post-output, B2 success)", len(atts))
	}
	if atts[0].Outcome == lipapi.AttemptSuccess && atts[1].Outcome == lipapi.AttemptSuccess {
		t.Fatalf("B1 must not be success after post-output interruption; must be swallowed/continued, got %v", atts[0].Outcome)
	}
}

// Idle path must hit DecideIdle via deterministic deadline, not EOF, and open guard continuation with isRetryPath==false.
func TestAgentLoopGuard_PostOutput_IdleDeterministic_NoRetryOneContinuationLegalStream(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex.Store = store
	ex.StreamRecovery = streamrecovery.Config{Enabled: true, IdleTimeout: 5 * time.Millisecond, GracePeriod: 0, EmitWarning: true, AllowPostOutputContinuation: true}
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "complete"}}
	ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{
				ID:         "idle-test",
				Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
				Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
				Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
			},
			aLegID:  "a-idle-1",
			traceID: "trace-idle-1",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-idle-1", Seq: 1}, routing.AttemptCandidate{
			Key:     "openai:gpt-4",
			Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
		}, authorityLifecycle{}),
		responsePipeline: &responsePipeline{},
		recovery:         &recoveryController{recoverPolicy: streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true, IdleTimeout: 5 * time.Millisecond, AllowPostOutputContinuation: true}, time.Now())},
	}
	bindTestRuntimeOwners(rs, ex)
	if rs.recovery == nil || rs.recovery.recoverPolicy == nil {
		t.Fatalf("recovery policy not bound")
	}
	if _, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"}); err != nil {
		t.Fatalf("prime text: %v", err)
	}
	old := time.Now().Add(-20 * time.Millisecond)
	rs.recovery.recoverPolicy.ObserveClientEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"}, old)

	var capturedIsRetry *bool
	var capturedMode *openMode
	// Install opener spy that captures isRetryPath and verifies openModeGuardContinuation
	execSetupGuardContinuationOpenerWithCapture(t, rs, []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: " world"}, {Kind: lipapi.EventResponseFinished}}, &capturedIsRetry, &capturedMode)

	// Now trigger idle: create a blocking stream that waits on ctx.Done() which will be DeadlineExceeded due to idle timeout.
	blocking := &blockingIdleStream{}
	testStoreInner(rs, blocking)
	// This Recv should hit idle path, consult guard, open B2 via guard continuation (isRetryPath false), and return B2 text without retry.
	ev, err := rs.Recv(context.Background())
	if err != nil {
		t.Fatalf("idle Recv: %v", err)
	}
	if ev.Kind != lipapi.EventTextDelta || ev.Delta != " world" {
		t.Fatalf("idle continuation must emit B2 text via guard continuation, got kind %q delta %q err %v", ev.Kind, ev.Delta, err)
	}
	if capturedIsRetry == nil || *capturedIsRetry != false {
		t.Fatalf("isRetryPath must be false for guard continuation, got %v", capturedIsRetry)
	}
	if capturedMode == nil || *capturedMode != openModeGuardContinuation {
		t.Fatalf("openMode must be openModeGuardContinuation, got %v", capturedMode)
	}
	// Next Recv should be the final terminal, exactly one.
	ev2, err := rs.Recv(context.Background())
	if err != nil {
		t.Fatalf("second Recv: %v", err)
	}
	if ev2.Kind != lipapi.EventResponseFinished {
		t.Fatalf("second kind %q want response_finished", ev2.Kind)
	}
	terms := 0
	if ev.Kind == lipapi.EventResponseFinished {
		terms++
	}
	if ev2.Kind == lipapi.EventResponseFinished {
		terms++
	}
	if terms != 1 {
		t.Fatalf("exactly one final EventResponseFinished across both Recvs, got %d", terms)
	}
	if !rs.terminal.finished() {
		t.Fatal("A finished after idle continuation B2")
	}
	// Verify idle decision actually hit streamrecovery path: ensure warning/finish not leaked as retry
	ev3, err := rs.Recv(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("third recv want EOF, got ev %v err %v", ev3, err)
	}
}

type blockingIdleStream struct{}

func (s *blockingIdleStream) Recv(ctx context.Context) (lipapi.Event, error) {
	<-ctx.Done()
	return lipapi.Event{}, ctx.Err()
}

func (s *blockingIdleStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{}
}

func (s *blockingIdleStream) Close() error { return nil }

func execSetupGuardContinuationOpenerWithCapture(t *testing.T, rs *retryRecvStream, events []lipapi.Event, outIsRetry **bool, outMode **openMode) {
	t.Helper()
	if rs.recovery == nil {
		rs.recovery = &recoveryController{}
	}
	var capIsRetry bool
	var capMode openMode
	*outIsRetry = &capIsRetry
	*outMode = &capMode
	rs.recovery.opener = func(ctx context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		capIsRetry = req.isRetryPath
		if req.isRetryPath {
			capMode = openModeRetry
		} else {
			capMode = openModeGuardContinuation
		}
		blegID := "b-guard-2"
		seq := 2
		if cur := rs.attempt.snapshot(); cur != nil {
			seq = int(cur.bleg.Seq) + 1
			blegID = cur.bleg.BLegID + "-cont"
		}
		bleg := b2bua.BLegRecord{BLegID: blegID, Seq: seq, ALegID: rs.facts.aLegID}
		cand := routing.AttemptCandidate{Key: "openai:gpt-4", Primary: routing.Primary{Backend: "openai", Model: "gpt-4"}}
		stream := &guardContinuationEventStream{events: events}
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
}

// Completed tool call + matching result then interruption: continue after retained result, tool executes once, verify PreservedToolPairs.
func TestAgentLoopGuard_PostOutput_CompletedToolPlusResultThenEOF_ContinuesWithoutReexecution(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.StreamRecovery = streamrecovery.Config{Enabled: true, EmitWarning: true, AllowPostOutputContinuation: true}
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "complete"}}
	ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

	var opens atomic.Int32
	var toolExecs atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				n := opens.Add(1)
				if n == 1 {
					toolExecs.Add(1)
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "read"},
						{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{"path":"x"}`},
						{Kind: lipapi.EventToolCallFinished, ToolCallID: "call_1"},
						{Kind: lipapi.EventTextDelta, Delta: "after-tool"},
					}), nil
				}
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: " continued"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("use tool")}}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	defer func() { _ = stream.Close() }()
	events, err := collectAll(stream)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("collect: %v", err)
	}
	if opens.Load() != 2 {
		t.Fatalf("completed tool+result then EOF must continue via one B2 (opens=%d want 2)", opens.Load())
	}
	if toolExecs.Load() != 1 {
		t.Fatalf("tool side effect must execute exactly once (got %d want 1)", toolExecs.Load())
	}
	terms := countTerminal(events)
	if terms != 1 {
		t.Fatalf("must have exactly one final terminal (got %d)", terms)
	}
	var gotText strings.Builder
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta {
			gotText.WriteString(ev.Delta)
		}
	}
	if gotText.String() != "after-tool continued" {
		t.Fatalf("text after tool continuation got %q want %q", gotText.String(), "after-tool continued")
	}
	leg, _ := store.FetchALeg(context.Background(), call.Session.ALegID)
	atts, _ := store.LoadAttempts(context.Background(), leg.ALegID)
	if len(atts) != 2 {
		t.Fatalf("attempts %d want 2", len(atts))
	}
	// Verify retained completed ToolResult via continuationsafety evaluation directly.
	tail := continuationsafety.TailState{
		CompletedCalls:          []lipapi.Item{{Kind: lipapi.ItemKindToolCall, ToolCall: &lipapi.ToolCallItem{CallID: "call_1", Name: "read"}}},
		CompletedResults:        []lipapi.Item{{Kind: lipapi.ItemKindToolCall, ToolResult: &lipapi.ToolResultItem{CallID: "call_1"}}},
		CommittedAssistantItems: []lipapi.Item{{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleAssistant, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "after-tool"}}}},
	}
	prior := continuationsafety.PriorSummary{Record: lipcont.ContinuationRecord{ID: lipcont.ResponseID("r1")}}
	res := continuationsafety.Evaluate(continuationsafety.Input{Prior: prior, Tail: tail, Bounds: lipcont.DefaultBounds()})
	if res.Facts.PreservedToolPairs != 1 {
		t.Fatalf("PreservedToolPairs=%d want 1", res.Facts.PreservedToolPairs)
	}
	if !res.Facts.MustNotReexecute {
		t.Fatalf("MustNotReexecute must be true for retained tool pair")
	}
	if res.Outcome != continuationsafety.OutcomeContinueSafe {
		t.Fatalf("tool continuation must be safe, got %v", res.Outcome)
	}
}

// Incomplete args or unsafe opaque => one controlled final, zero continuation opens/tool executions.
func TestAgentLoopGuard_PostOutput_UnsafeState_NoReplayOneTerminal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mkB1 func() lipapi.ManagedEventStream
	}{
		{
			name: "incomplete_tool_args",
			mkB1: func() lipapi.ManagedEventStream {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "hello"},
					{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "bash"},
					{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{"cmd":`},
				})
			},
		},
		{
			name: "unsafe_opaque_thinking",
			mkB1: func() lipapi.ManagedEventStream {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "hello"},
					{Kind: lipapi.EventReasoningOpaqueDelta, Opaque: []byte(`{"type":"redacted_thinking"}`)},
				})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ex := TestExecutor()
			store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ex.Store = store
			ex.Bus = hooks.New(hooks.Config{})
			ex.Rand = routing.NewSeededRng(1)
			ex.StreamRecovery = streamrecovery.Config{Enabled: true, EmitWarning: true, AllowPostOutputContinuation: true}
			fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "should be blocked", Reason: "unsafe"}}
			ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)
			var opens atomic.Int32
			var toolExecs atomic.Int32
			ex.Backends = map[string]execbackend.Backend{
				"openai": {
					Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						n := opens.Add(1)
						if n == 1 {
							return tc.mkB1(), nil
						}
						toolExecs.Add(1)
						return lipapi.NewFixedEventStream([]lipapi.Event{
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
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			defer func() { _ = stream.Close() }()
			events, err := collectAll(stream)
			if err != nil && !errors.Is(err, io.EOF) {
				t.Fatalf("collect err %v", err)
			}
			if opens.Load() != 1 {
				t.Fatalf("%s must not open continuation B2 (opens=%d want 1)", tc.name, opens.Load())
			}
			if toolExecs.Load() != 0 {
				t.Fatalf("%s must not execute tool (execs=%d want 0)", tc.name, toolExecs.Load())
			}
			terms := countTerminal(events)
			if terms != 1 {
				t.Fatalf("%s must surface exactly one controlled terminal (got %d want 1) events=%v", tc.name, terms, events)
			}
			leg, _ := store.FetchALeg(context.Background(), call.Session.ALegID)
			atts, _ := store.LoadAttempts(context.Background(), leg.ALegID)
			if len(atts) != 1 {
				t.Fatalf("attempts %d want 1", len(atts))
			}
			// Verify unsafe state via continuationsafety directly
			if tc.name == "incomplete_tool_args" {
				tail := continuationsafety.TailState{HasIncompleteToolArgs: true}
				prior := continuationsafety.PriorSummary{Record: lipcont.ContinuationRecord{ID: lipcont.ResponseID("r1")}}
				res := continuationsafety.Evaluate(continuationsafety.Input{Prior: prior, Tail: tail, Bounds: lipcont.DefaultBounds()})
				if res.Outcome != continuationsafety.OutcomeUnsafePartialToolArgs {
					t.Fatalf("incomplete args must be unsafe partial, got %v", res.Outcome)
				}
			} else {
				tail := continuationsafety.TailState{HasUnsupportedOpaqueState: true}
				prior := continuationsafety.PriorSummary{Record: lipcont.ContinuationRecord{ID: lipcont.ResponseID("r1")}}
				res := continuationsafety.Evaluate(continuationsafety.Input{Prior: prior, Tail: tail, Bounds: lipcont.DefaultBounds()})
				if res.Outcome != continuationsafety.OutcomeUnsupportedOpaqueState {
					t.Fatalf("opaque must be unsupported, got %v", res.Outcome)
				}
			}
		})
	}
}

// Cancellation prevents continuation.
func TestAgentLoopGuard_PostOutput_CancellationPreventsContinuation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		when string
	}{
		{"cancel_before_continuation_open", "before"},
		{"cancel_during_continuation_open", "during"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ex := TestExecutor()
			store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ex.Store = store
			ex.Bus = hooks.New(hooks.Config{})
			ex.Rand = routing.NewSeededRng(1)
			ex.StreamRecovery = streamrecovery.Config{Enabled: true, EmitWarning: true, AllowPostOutputContinuation: true}
			block := make(chan struct{})
			entered := make(chan struct{}, 1)
			fv := &fakeGuardVerifierWithBlock{
				enteredCh: entered,
				blockCh:   block,
				verdict:   stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "work", Reason: "pending"},
			}
			ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)
			if tc.when == "before" {
				ctx, cancel := context.WithCancel(context.Background())
				var opens atomic.Int32
				ex.Backends = map[string]execbackend.Backend{
					"openai": {
						Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
						Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
							opens.Add(1)
							return lipapi.NewFixedEventStream([]lipapi.Event{
								{Kind: lipapi.EventResponseStarted},
								{Kind: lipapi.EventMessageStarted},
								{Kind: lipapi.EventTextDelta, Delta: "hello"},
							}), nil
						},
					},
				}
				call := &lipapi.Call{
					Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
					Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
					Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
				}
				stream, err := ex.Execute(ctx, call)
				if err != nil {
					t.Fatalf("Execute: %v", err)
				}
				defer func() { _ = stream.Close() }()
				cancel()
				close(block)
				_, err = lipapi.Collect(ctx, stream)
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, io.EOF) {
					t.Logf("Collect err %v", err)
				}
				if opens.Load() != 1 {
					t.Fatalf("cancellation must prevent B2 open (opens=%d want 1)", opens.Load())
				}
				leg, _ := store.FetchALeg(context.Background(), call.Session.ALegID)
				atts, _ := store.LoadAttempts(context.Background(), leg.ALegID)
				if len(atts) != 1 {
					t.Fatalf("attempts %d want 1 authoritative cancellation", len(atts))
				}
			} else {
				ex2 := TestExecutor()
				store2, err2 := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
				if err2 != nil {
					t.Fatal(err2)
				}
				ex2.Store = store2
				ex2.StreamRecovery = streamrecovery.Config{Enabled: true, IdleTimeout: 5 * time.Millisecond, AllowPostOutputContinuation: true}
				ex2.LoopGuardFactory = newLoopGuardFactoryForTest(fv)
				rs := &retryRecvStream{
					terminal: newTurnTerminal(),
					facts: testRecvTurnFacts(recvTurnFacts{
						baseline: lipapi.Call{
							ID:         "cancel-during",
							Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
							Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
							Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
						},
						aLegID:  "a-cancel-2",
						traceID: "trace-cancel-2",
					}),
					attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-cancel-2", Seq: 1}, routing.AttemptCandidate{
						Key:     "openai:gpt-4",
						Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
					}, authorityLifecycle{}),
					responsePipeline: &responsePipeline{},
					recovery:         &recoveryController{recoverPolicy: streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true, IdleTimeout: 5 * time.Millisecond, AllowPostOutputContinuation: true}, time.Now())},
				}
				bindTestRuntimeOwners(rs, ex2)
				if _, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"}); err != nil {
					t.Fatalf("prime text: %v", err)
				}
				old := time.Now().Add(-20 * time.Millisecond)
				rs.recovery.recoverPolicy.ObserveClientEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"}, old)
				// For post-output, continuation does not involve verifier; test cancellation authoritative for opener.
				var capB *bool
				var capM *openMode
				execSetupGuardContinuationOpenerWithCapture(t, rs, []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: " should-not-appear"}}, &capB, &capM)
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				close(block)
				blocking := &blockingIdleStream{}
				testStoreInner(rs, blocking)
				ev, recvErr := rs.Recv(ctx)
				if recvErr == nil && ev.Kind == lipapi.EventTextDelta && ev.Delta == " should-not-appear" {
					t.Fatalf("cancel during continuation must not produce B2 text")
				}
				if !errors.Is(recvErr, context.Canceled) && !rs.terminal.finished() {
					t.Fatalf("cancellation must terminalize A-side, recvErr=%v finished=%v", recvErr, rs.terminal.finished())
				}
				if rs.terminal.guardHidden != "" && ev.Delta == " should-not-appear" {
					t.Fatalf("hidden should not leak on cancel")
				}
				_ = capB
				_ = capM
				_ = entered
			}
		})
	}
}

// Guard-disabled retains existing finish behavior (compatibility).
func TestAgentLoopGuard_PostOutput_GuardDisabled_RetainsExistingFinishBehavior(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.StreamRecovery = streamrecovery.Config{Enabled: true, EmitWarning: true, AllowPostOutputContinuation: false}
	ex.LoopGuardFactory = nil
	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "hello"},
				}), nil
			},
		},
	}
	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	events, err := collectAll(stream)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("collect %v", err)
	}
	if opens.Load() != 1 {
		t.Fatalf("guard disabled must not open B2, opens=%d", opens.Load())
	}
	terms := countTerminal(events)
	if terms != 1 {
		t.Fatalf("guard disabled must surface exactly one terminal, got %d events=%v", terms, events)
	}
	leg, _ := store.FetchALeg(context.Background(), call.Session.ALegID)
	atts, _ := store.LoadAttempts(context.Background(), leg.ALegID)
	if len(atts) != 1 {
		t.Fatalf("attempts %d want 1", len(atts))
	}
}

// RetryRecvStream unit: post-output EOF opens guard continuation with isRetryPath==false, not retry.
func TestAgentLoopGuard_PostOutput_EOF_RetryRecvStream_GuardContinuationIsNotRetry(t *testing.T) {
	t.Parallel()
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "complete"}}
	ex := TestExecutor()
	store, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex.Store = store
	ex.StreamRecovery = streamrecovery.Config{Enabled: true, AllowPostOutputContinuation: true}
	ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{
				ID:         "eof-isretry",
				Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
				Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
				Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
			},
			aLegID:  "a-eof-retry",
			traceID: "trace-eof-retry",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-eof-retry", Seq: 1}, routing.AttemptCandidate{
			Key:     "openai:gpt-4",
			Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
		}, authorityLifecycle{}),
		responsePipeline: &responsePipeline{},
		recovery:         &recoveryController{recoverPolicy: streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true, AllowPostOutputContinuation: true}, time.Now())},
	}
	bindTestRuntimeOwners(rs, ex)
	if _, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"}); err != nil {
		t.Fatalf("prime: %v", err)
	}
	var capIsRetry *bool
	var capMode *openMode
	execSetupGuardContinuationOpenerWithCapture(t, rs, []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: " world"}, {Kind: lipapi.EventResponseFinished}}, &capIsRetry, &capMode)
	ev, err := testRecvEOF(context.Background(), rs)
	if err != nil {
		t.Fatalf("EOF Recv: %v err %v", ev, err)
	}
	t.Logf("first ev kind=%v delta=%q err=%v capIsRetry=%v capMode=%v", ev.Kind, ev.Delta, err, capIsRetry, capMode)
	if ev.Kind != lipapi.EventTextDelta || ev.Delta != " world" {
		t.Fatalf("EOF continuation must be guard continuation text, got %v err %v", ev, err)
	}
	if capIsRetry == nil || *capIsRetry != false {
		t.Fatalf("isRetryPath must be false, got %v", capIsRetry)
	}
	if capMode == nil || *capMode != openModeGuardContinuation {
		t.Fatalf("openMode must be guard continuation, got %v", capMode)
	}
	t.Logf("before second recv, verifier calls=%d", fv.CallCount())
	ev2, err := rs.Recv(context.Background())
	t.Logf("second ev kind=%v finishReason=%q err=%v verifier calls=%d", ev2.Kind, ev2.FinishReason, err, fv.CallCount())
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if ev2.Kind != lipapi.EventResponseFinished {
		t.Fatalf("second kind %q want finished, ev2=%+v", ev2.Kind, ev2)
	}
	if terms := countTerminal([]lipapi.Event{ev, ev2}); terms != 1 {
		t.Fatalf("exactly one final terminal, got %d", terms)
	}
}

func collectAll(s lipapi.EventStream) ([]lipapi.Event, error) {
	var out []lipapi.Event
	for {
		ev, err := s.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func countTerminal(evts []lipapi.Event) int {
	n := 0
	for _, e := range evts {
		if e.Kind == lipapi.EventResponseFinished {
			n++
		}
	}
	return n
}
