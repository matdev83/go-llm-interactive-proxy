package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// TestAgentLoopGuard_ExplicitCompletion_MissingSeam_RED documents the narrow
// missing seam where 6.x hardcodes ExplicitCompletion:false in
// agentLoopGuardHoldCandidate and terminal facts. This is compile-safe RED
// pending 8.3 canonical plumbing.
func TestAgentLoopGuard_ExplicitCompletion_MissingSeam_RED(t *testing.T) {
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
	// Authoritative HasExplicitCompletion requires correlated completed result
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
	// Generic finish must be rejected even when correlated
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

	t.Run("trust_RED_still_calls_verifier", func(t *testing.T) {
		t.Parallel()
		fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop}}
		_, rs, _ := setupGuardedStream(t, fv, true)
		initial := fv.CallCount()
		_, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished})
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if fv.CallCount() == initial {
			t.Logf("GREEN: seam fixed — trust+explicit now skips verifier")
		} else {
			t.Errorf("RED seam pending 8.3: trust+explicit should skip verifier (want %d calls, got %d) — runtime hardcodes ExplicitCompletion:false", initial, fv.CallCount())
		}
	})

	t.Run("verify_RED_missing_evidence", func(t *testing.T) {
		t.Parallel()
		t.Errorf("RED seam pending 8.3: runtime never sets Candidate.ExplicitCompletion from lipapi canonical fact; verify policy cannot receive ExplicitCompletion=true")
	})
}
