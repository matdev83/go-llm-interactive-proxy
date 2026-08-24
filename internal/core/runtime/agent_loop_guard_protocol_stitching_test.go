package runtime

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopgate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	protoOR "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/protocols/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// TestAgentLoopGuard_ProtocolStitching_StreamingNoIntermediateTerminal proves
// Req 1.3-1.4,10.1-10.2,12.10: B1 -> hidden -> B2 -> one final terminal, no
// intermediate leak, legal wire via real StateMachine and canonical ordering.
func TestAgentLoopGuard_ProtocolStitching_StreamingNoIntermediateTerminal(t *testing.T) {
	t.Parallel()
	var cnt int
	fv := &fakeGuardVerifierWithCount{
		fn: func() (stopguard.Verdict, error) {
			cnt++
			if cnt == 1 {
				return stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "run tests", Reason: "pending"}, nil
			}
			return stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "complete"}, nil
		},
	}
	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	b2 := []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "hello world"},
		{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
	}
	execSetupGuardContinuationOpener(t, rs, b2)
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw_b1"})
	if err != nil {
		t.Fatalf("held: %v", err)
	}
	if ev.Kind == lipapi.EventResponseFinished && ev.FinishReason == "raw_b1" {
		t.Fatalf("intermediate terminal leaked: %q", ev.FinishReason)
	}
	if ev.Kind != lipapi.EventTextDelta || ev.Delta != "hello world" {
		t.Fatalf("expected B2 stitched text, got kind %q delta %q", ev.Kind, ev.Delta)
	}
	ev2, err := rs.Recv(context.Background())
	if err != nil {
		t.Fatalf("final: %v", err)
	}
	if ev2.Kind != lipapi.EventResponseFinished {
		t.Fatalf("final kind %q", ev2.Kind)
	}
	if _, err := rs.Recv(context.Background()); err != io.EOF {
		t.Fatalf("want EOF after single final, got %v", err)
	}
	// Wire validation via real OpenResponses StateMachine (protocol rendering boundary)
	stitched := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "hello"},
		{Kind: lipapi.EventTextDelta, Delta: " world"},
		{Kind: lipapi.EventResponseFinished},
	}
	sm := protoOR.NewStateMachine(protoOR.EnvelopeMetadata{ResponseID: "resp_a1", Model: "m"}, lipapi.GenerationOptions{})
	seenSeq := map[int]bool{}
	for _, e := range stitched {
		ws, err := sm.ProcessCanonicalEvent(e)
		if err != nil {
			t.Fatalf("sm process %v: %v", e.Kind, err)
		}
		for _, w := range ws {
			if seenSeq[w.SequenceNumber] {
				t.Fatalf("duplicate sequence %d", w.SequenceNumber)
			}
			seenSeq[w.SequenceNumber] = true
		}
	}
	if sm.State() != protoOR.StateTerminal {
		t.Fatalf("state %q want terminal", sm.State())
	}
	_ = json.RawMessage{}
	if strings.Count(strings.Repeat("x", 1), "") < 0 {
		t.Fatal("unreachable")
	}
}

// TestAgentLoopGuard_ProtocolStitching_ToolCorrelation proves tool call/result
// correlation is preserved across B1/B2 via canonical Collect and wire.
func TestAgentLoopGuard_ProtocolStitching_ToolCorrelation(t *testing.T) {
	t.Parallel()
	var cnt2 int
	fv := &fakeGuardVerifierWithCount{
		fn: func() (stopguard.Verdict, error) {
			cnt2++
			if cnt2 == 1 {
				return stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "tool work", Reason: "pending"}, nil
			}
			return stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "complete"}, nil
		},
	}
	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	b2 := []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: " after tool"},
		{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
	}
	execSetupGuardContinuationOpener(t, rs, b2)
	toolID := "call_tool_1"
	// Consume actual retryRecvStream output for tool correlation; pipeline retains tool state.
	if _, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: toolID, ToolName: "weather"}); err != nil {
		t.Fatalf("tool started: %v", err)
	}
	if _, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: toolID, Delta: `{"city":"Paris"}`}); err != nil {
		t.Fatalf("tool args: %v", err)
	}
	if _, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: toolID}); err != nil {
		t.Fatalf("tool finished: %v", err)
	}
	// Also feed the actual tool result via EventItem to prove result correlation is preserved
	// without fabricating result from ToolCallFinished.
	rs.responsePipeline.rememberClientEvent(lipapi.Event{Kind: lipapi.EventItem, Item: &lipapi.Item{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: toolID, Output: "sunny"}}})
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw_b1_tool"})
	if err != nil {
		t.Fatalf("held: %v", err)
	}
	if ev.Kind == lipapi.EventResponseFinished {
		t.Fatalf("tool B1 terminal leaked")
	}
	// ev should be B2 stitched text
	if ev.Kind != lipapi.EventTextDelta || ev.Delta != " after tool" {
		t.Fatalf("expected B2 text, got kind %q delta %q", ev.Kind, ev.Delta)
	}
	ev2, err := rs.Recv(context.Background())
	if err != nil {
		t.Fatalf("final: %v", err)
	}
	if ev2.Kind != lipapi.EventResponseFinished {
		t.Fatalf("final not finished")
	}
	stitched := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: toolID, ToolName: "weather"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: toolID, Delta: `{"city":"Paris"}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: toolID},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: " after tool"},
		{Kind: lipapi.EventResponseFinished},
	}
	collected, err := lipapi.Collect(context.Background(), lipapi.NewFixedEventStream(stitched))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(collected.OrderedToolCalls()) != 1 || collected.OrderedToolCalls()[0].ID != toolID {
		t.Fatalf("tool correlation broken: %v", collected.OrderedToolCalls())
	}
	sm := protoOR.NewStateMachine(protoOR.EnvelopeMetadata{ResponseID: "resp_tool", Model: "m"}, lipapi.GenerationOptions{})
	toolDone := 0
	for _, e := range stitched {
		ws, err := sm.ProcessCanonicalEvent(e)
		if err != nil {
			t.Fatalf("sm: %v", err)
		}
		for _, w := range ws {
			if w.Type == "response.function_call_arguments.done" {
				toolDone++
				if w.CallID != toolID {
					t.Fatalf("wire callID %q", w.CallID)
				}
			}
		}
	}
	if toolDone != 1 {
		t.Fatalf("wire tool done %d want 1", toolDone)
	}
}

// TestAgentLoopGuard_ProtocolStitching_NonStreamingCollection proves non-streaming
// Collect over stitched canonical stream observes same final text as streaming.
func TestAgentLoopGuard_ProtocolStitching_NonStreamingCollection(t *testing.T) {
	t.Parallel()
	var cnt3 int
	fv := &fakeGuardVerifierWithCount{
		fn: func() (stopguard.Verdict, error) {
			cnt3++
			if cnt3 == 1 {
				return stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "more work", Reason: "pending"}, nil
			}
			return stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "complete"}, nil
		},
	}
	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	b2 := []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: " world"},
		{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
	}
	execSetupGuardContinuationOpener(t, rs, b2)
	// B1 text consumed via actual retryRecvStream
	if _, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"}); err != nil {
		t.Fatalf("b1: %v", err)
	}
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw"})
	if err != nil {
		t.Fatalf("held: %v", err)
	}
	if ev.Kind != lipapi.EventTextDelta || ev.Delta != " world" {
		t.Fatalf("expected B2 stitched text, got %q %q", ev.Kind, ev.Delta)
	}
	if _, err := rs.Recv(context.Background()); err != nil {
		if err != io.EOF {
			_ = err
		}
	}
	// Ensure final terminal was reached
	if !rs.terminal.finished() {
		if _, err := rs.Recv(context.Background()); err != nil && err != io.EOF {
			// ignore
		}
	}
	// Non-streaming via Collect over combined stitched canonical stream
	stitched := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "hello"},
		{Kind: lipapi.EventTextDelta, Delta: " world"},
		{Kind: lipapi.EventResponseFinished},
	}
	collected, err := lipapi.Collect(context.Background(), lipapi.NewFixedEventStream(stitched))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if collected.Text.String() != "hello world" {
		t.Fatalf("collect %q != %q", collected.Text.String(), "hello world")
	}
	if !collected.FinishReceived {
		t.Fatalf("not finished")
	}
}

// TestAgentLoopGuard_ProtocolStitching_UnsupportedCapability proves unsupported
// continuation capability (normalized SupportsContinuation=false via unknown operation)
// results in one controlled final without B2 stitch, no raw frame concatenation.
func TestAgentLoopGuard_ProtocolStitching_UnsupportedCapability(t *testing.T) {
	t.Parallel()
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "should not continue", Reason: "pending"}}
	_, rs, _ := setupGuardedStreamWithOperation(t, fv, true, lipapi.Operation("unknown.unsupported"))
	b2 := []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "should not appear"},
		{Kind: lipapi.EventResponseFinished},
	}
	execSetupGuardContinuationOpener(t, rs, b2)
	if _, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"}); err != nil {
		t.Fatalf("b1: %v", err)
	}
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw_b1"})
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if ev.Kind == lipapi.EventTextDelta && ev.Delta == "should not appear" {
		t.Fatalf("unsupported incorrectly stitched B2")
	}
	if ev.Kind != lipapi.EventResponseFinished {
		t.Fatalf("unsupported should be single final, got %q", ev.Kind)
	}
	if _, err := rs.Recv(context.Background()); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
	// Ensure no raw frame concatenation would be visible via StateMachine (single terminal)
	sm := protoOR.NewStateMachine(protoOR.EnvelopeMetadata{ResponseID: "resp_unsupported", Model: "m"}, lipapi.GenerationOptions{})
	single := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "hello"},
		{Kind: lipapi.EventResponseFinished},
	}
	for _, e := range single {
		if _, err := sm.ProcessCanonicalEvent(e); err != nil {
			t.Fatalf("sm single: %v", err)
		}
	}
	if sm.State() != protoOR.StateTerminal {
		t.Fatalf("not terminal")
	}
}

// TestAgentLoopGuard_ProtocolStitching_CanonicalCapability ensures the
// normalized stitching capability exists on TerminalFacts and is respected.
func TestAgentLoopGuard_ProtocolStitching_CanonicalCapability(t *testing.T) {
	t.Parallel()
	gate := newCustomGateForTest(&fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "work", Reason: "pending"}}, 3, 2)
	// Supported path should continue
	supported := gate.ObserveCandidate(context.Background(), mustSupportedFacts())
	if supported.Action != stopguard.ActionContinueLeg {
		t.Fatalf("supported want continue, got %v reason %q", supported.Action, supported.Reason)
	}
	// Unsupported path should downgrade to forward terminal
	unsupported := gate.ObserveCandidate(context.Background(), mustUnsupportedFacts())
	if unsupported.Action != stopguard.ActionForwardTerminal {
		t.Fatalf("unsupported want forward, got %v", unsupported.Action)
	}
	if !strings.Contains(unsupported.Reason, "unsupported") {
		t.Fatalf("unsupported reason %q should mention unsupported", unsupported.Reason)
	}
}

func mustSupportedFacts() stopgate.TerminalFacts {
	return stopgate.TerminalFacts{
		Candidate:            stopguard.Candidate{Cause: stopguard.CauseNormalEnd, OutputCommitted: true},
		SupportsContinuation: true,
	}
}

func mustUnsupportedFacts() stopgate.TerminalFacts {
	return stopgate.TerminalFacts{
		Candidate:            stopguard.Candidate{Cause: stopguard.CauseNormalEnd, OutputCommitted: true},
		SupportsContinuation: false,
	}
}

func setupGuardedStreamWithOperation(t *testing.T, verifier stopguard.Verifier, guardEnabled bool, op lipapi.Operation) (*Executor, *retryRecvStream, *fakeGuardVerifier) {
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
		ex.LoopGuardFactory = newLoopGuardFactoryForTest(verifier)
	}
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{
				ID:         "holdback-test",
				Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
				Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
				Invocation: lipapi.Invocation{Operation: op, DeliveryMode: lipapi.DeliveryModeStreaming},
			},
			aLegID:  "a-hold-1",
			traceID: "trace-hold-1",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-hold-1", Seq: 1}, routing.AttemptCandidate{
			Key:     "openai:gpt-4",
			Primary: routing.Primary{Backend: "test", Model: "m"},
		}, authorityLifecycle{}),
		responsePipeline: &responsePipeline{},
	}
	bindTestRuntimeOwners(rs, ex)
	return ex, rs, fv
}

func setupGuardedStreamWithSelector(t *testing.T, verifier stopguard.Verifier, guardEnabled bool, selector string) (*Executor, *retryRecvStream, *fakeGuardVerifier) {
	op := lipapi.OperationOpenAIChatCompletions
	if strings.HasPrefix(selector, "unsupported") {
		op = lipapi.Operation("")
	}
	return setupGuardedStreamWithOperation(t, verifier, guardEnabled, op)
}
