package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopgate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// TestAgentLoopGuard_ExplicitCompletion_MissingSeam documents the canonical explicit
// completion plumbing: trust skips verifier when correlated call+result present,
// verify still passes explicit evidence, malformed/absent falls back.
func TestAgentLoopGuard_ExplicitCompletion_MissingSeam(t *testing.T) {
	t.Parallel()

	explicitItem := lipapi.Item{
		Kind: lipapi.ItemKindToolCall,
		ToolCall: &lipapi.ToolCallItem{
			CallID:    "call-1",
			Name:      "attempt_completion",
			Arguments: json.RawMessage(`{"result":"done"}`),
		},
	}
	if !lipapi.IsExplicitCompletionItem(explicitItem) {
		t.Fatal("fixture: IsExplicitCompletionItem must recognize attempt_completion name")
	}
	if lipapi.HasExplicitCompletion([]lipapi.Item{explicitItem}) {
		t.Fatal("fixture: HasExplicitCompletion must be false for call-only (no executed evidence)")
	}
	correlated := []lipapi.Item{
		{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusCompleted, ToolCall: &lipapi.ToolCallItem{CallID: "call-1", Name: "attempt_completion", Arguments: json.RawMessage(`{"result":"done"}`)}},
		{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: "call-1", Name: "attempt_completion", Output: "ok"}},
	}
	if !lipapi.HasExplicitCompletion(correlated) {
		t.Fatal("fixture: HasExplicitCompletion must be true for correlated completed call+result")
	}
	finishCorrelated := []lipapi.Item{
		{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusCompleted, ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "finish"}},
		{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: "c1", Name: "finish", Output: "ok"}},
	}
	if lipapi.HasExplicitCompletion(finishCorrelated) {
		t.Fatal("fixture: generic finish must be rejected even when correlated")
	}
	malformed := lipapi.Item{Kind: lipapi.ItemKindToolCall, ToolCall: &lipapi.ToolCallItem{CallID: "", Name: "attempt_completion"}}
	if lipapi.IsExplicitCompletionItem(malformed) {
		t.Fatal("malformed explicit item must be false")
	}

	t.Run("trust_skips_verifier_when_explicit", func(t *testing.T) {
		t.Parallel()
		fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop}}
		_, rs, _ := setupGuardedStream(t, fv, true)
		// Correlated explicit completion requires ToolCall + matching ToolResult EventItem with completed status.
		rs.responsePipeline.rememberClientEvent(lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "call-1", ToolName: "attempt_completion"})
		rs.responsePipeline.rememberClientEvent(lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: "call-1"})
		rs.responsePipeline.rememberClientEvent(lipapi.Event{Kind: lipapi.EventItem, Item: &lipapi.Item{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: "call-1", Name: "attempt_completion", Output: "ok"}}})
		initial := fv.CallCount()
		_, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if fv.CallCount() != initial {
			t.Fatalf("trust+explicit should skip verifier (want %d calls, got %d)", initial, fv.CallCount())
		}
	})

	t.Run("verify_passes_explicit_evidence", func(t *testing.T) {
		t.Parallel()
		spy := &spyGuardVerifier2{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "complete"}}
		_, rs, _ := setupGuardedStreamWithPolicy(t, spy, stopguard.PolicyVerify)
		rs.responsePipeline.rememberClientEvent(lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "call-1", ToolName: "attempt_complete"})
		rs.responsePipeline.rememberClientEvent(lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: "call-1"})
		rs.responsePipeline.rememberClientEvent(lipapi.Event{Kind: lipapi.EventItem, Item: &lipapi.Item{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: "call-1", Name: "attempt_complete", Output: "ok"}}})
		_, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if !spy.captured.ExplicitCompletion {
			t.Fatalf("verify policy should receive ExplicitCompletion=true, got false")
		}
	})

	t.Run("started_finished_without_result_calls_verifier", func(t *testing.T) {
		t.Parallel()
		spy := &spyGuardVerifier2{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop}}
		_, rs, _ := setupGuardedStreamWithPolicy(t, spy, stopguard.PolicyTrust)
		// Started+Finished without result is NOT explicit completion; trust must still call verifier.
		rs.responsePipeline.rememberClientEvent(lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "call-1", ToolName: "attempt_completion"})
		rs.responsePipeline.rememberClientEvent(lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: "call-1"})
		// No EventItem result => HasExplicitCompletion false
		_, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if spy.captured.ExplicitCompletion {
			t.Fatalf("without result explicit should be false")
		}
		if spy.called == 0 {
			t.Fatalf("Started+Finished without result should call verifier (trust does not skip)")
		}
	})

	t.Run("malformed_falls_back", func(t *testing.T) {
		t.Parallel()
		spy := &spyGuardVerifier2{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop}}
		_, rs, _ := setupGuardedStreamWithPolicy(t, spy, stopguard.PolicyTrust)
		// Malformed: call without result should not be explicit
		rs.responsePipeline.rememberClientEvent(lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "call-1", ToolName: "attempt_completion"})
		// No finished -> tail will have incomplete, HasExplicitCompletion false
		_, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if spy.captured.ExplicitCompletion {
			t.Fatalf("malformed explicit should be false")
		}
		// For trust, malformed should still go through verifier (since not explicit)
		if spy.called == 0 {
			t.Fatalf("malformed should have invoked verifier")
		}
	})
}

type spyGuardVerifier2 struct {
	captured stopguard.Evidence
	verdict  stopguard.Verdict
	err      error
	called   int
}

func (s *spyGuardVerifier2) Verify(_ context.Context, ev stopguard.Evidence) (stopguard.Verdict, error) {
	s.captured = ev
	s.called++
	if s.err != nil {
		return stopguard.Verdict{Kind: stopguard.VerdictUncertain}, s.err
	}
	return s.verdict, nil
}

func setupGuardedStreamWithPolicy(t *testing.T, verifier stopguard.Verifier, policy stopguard.ExplicitCompletionPolicy) (*Executor, *retryRecvStream, stopguard.Verifier) {
	t.Helper()
	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex.Store = store
	ex.LoopGuardFactory = NewLoopGuardFactory(stopgate.Ports{Verifier: verifier, Now: time.Now}, stopgate.Config{
		Enabled: true, ExplicitCompletionPolicy: policy, MaxSemanticContinuations: 3, NoProgressLimit: 2,
	})
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{
				ID:         "guard-gate-test",
				Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
				Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
				Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
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
	return ex, rs, verifier
}
