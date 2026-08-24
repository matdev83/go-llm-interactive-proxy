package stopgate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func correlatedExplicitItems(callID string) []lipapi.Item {
	return []lipapi.Item{
		{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusCompleted, ToolCall: &lipapi.ToolCallItem{CallID: callID, Name: "attempt_completion", Arguments: json.RawMessage(`{"result":"done"}`)}},
		{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: callID, Name: "attempt_completion", Output: "ok"}},
	}
}

func callOnlyItems(callID string) []lipapi.Item {
	return []lipapi.Item{
		{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusCompleted, ToolCall: &lipapi.ToolCallItem{CallID: callID, Name: "attempt_completion"}},
	}
}

// TestGate_ExplicitCompletion_TrustSkipsVerifierAndReleasesTerminal proves
// PolicyTrust + valid explicit completion => no verifier call/no hidden
// semantic continuation, terminal released normally.
func TestGate_ExplicitCompletion_TrustSkipsVerifierAndReleasesTerminal(t *testing.T) {
	t.Parallel()
	fv := &fakeVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "should not be called"}}
	gate := New(Ports{Verifier: fv}, Config{
		Enabled:                  true,
		ExplicitCompletionPolicy: stopguard.PolicyTrust,
		MaxSemanticContinuations: 3,
		NoProgressLimit:          2,
	})
	items := correlatedExplicitItems("c1")
	explicit := lipapi.HasExplicitCompletion(items)
	require.True(t, explicit, "correlated call+result must be explicit completion")
	cand := stopguard.Candidate{Cause: stopguard.CauseNormalEnd, ExplicitCompletion: explicit, OutputCommitted: true}
	facts := terminalFacts(cand)
	out := gate.ObserveCandidate(context.Background(), facts)
	assert.Equal(t, stopguard.ActionForwardTerminal, out.Action)
	assert.True(t, out.HoldReleased)
	assert.True(t, out.AttemptSettledOnce)
	assert.Equal(t, 0, fv.CallCount(), "trust policy must not invoke verifier for clean explicit completion")
}

// TestGate_ExplicitCompletion_VerifyPassesStrongEvidence proves PolicyVerify +
// valid explicit completion => verifier invoked once with Evidence.ExplicitCompletion=true.
func TestGate_ExplicitCompletion_VerifyPassesStrongEvidence(t *testing.T) {
	t.Parallel()
	fv := &fakeVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop}}
	gate := New(Ports{Verifier: fv}, Config{
		Enabled:                  true,
		ExplicitCompletionPolicy: stopguard.PolicyVerify,
		MaxSemanticContinuations: 3,
		NoProgressLimit:          2,
	})
	items := correlatedExplicitItems("c1")
	explicit := lipapi.HasExplicitCompletion(items)
	require.True(t, explicit)
	cand := stopguard.Candidate{Cause: stopguard.CauseNormalEnd, ExplicitCompletion: explicit, OutputCommitted: true}
	facts := terminalFacts(cand)
	out := gate.ObserveCandidate(context.Background(), facts)
	require.Equal(t, 1, fv.CallCount(), "verify policy must invoke verifier exactly once even with explicit completion")
	ev, ok := fv.LastEvidence()
	require.True(t, ok)
	assert.True(t, ev.ExplicitCompletion, "verify policy must pass ExplicitCompletion=true as strong evidence")
	assert.Equal(t, stopguard.CauseNormalEnd, ev.Cause)
	assert.Equal(t, stopguard.ActionForwardTerminal, out.Action)
	assert.True(t, out.HoldReleased)
}

// TestGate_ExplicitCompletion_MalformedFallsBackToNormalSemanticPolicy proves
// malformed/absent explicit signal => false and normal verifier behavior.
// Call-only is malformed for authoritative HasExplicitCompletion.
func TestGate_ExplicitCompletion_MalformedFallsBackToNormalSemanticPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		items []lipapi.Item
	}{
		{"absent", []lipapi.Item{{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleAssistant, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "done"}}}}},
		{"call only without result", callOnlyItems("c1")},
		{"orphan result", []lipapi.Item{{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: "c1", Name: "attempt_completion", Output: "ok"}}}},
		{"generic finish correlated rejected", []lipapi.Item{
			{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusCompleted, ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "finish"}},
			{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: "c1", Name: "finish", Output: "ok"}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			explicit := lipapi.HasExplicitCompletion(tc.items)
			assert.False(t, explicit, "malformed/absent must be false via HasExplicitCompletion")
			fv := &fakeVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop}}
			gate := New(Ports{Verifier: fv}, Config{
				Enabled:                  true,
				ExplicitCompletionPolicy: stopguard.PolicyTrust,
				MaxSemanticContinuations: 3,
				NoProgressLimit:          2,
			})
			cand := stopguard.Candidate{Cause: stopguard.CauseNormalEnd, ExplicitCompletion: explicit, OutputCommitted: true}
			facts := terminalFacts(cand)
			out := gate.ObserveCandidate(context.Background(), facts)
			_ = out
			assert.Equal(t, 1, fv.CallCount(), "malformed/absent must fall back to normal verifier behavior")
			ev, ok := fv.LastEvidence()
			require.True(t, ok)
			assert.False(t, ev.ExplicitCompletion)
		})
	}
}

// TestGate_ExplicitCompletion_VerifyMalformedStillFalse ensures verify policy
// also treats malformed as false evidence.
func TestGate_ExplicitCompletion_VerifyMalformedStillFalse(t *testing.T) {
	t.Parallel()
	items := callOnlyItems("c1")
	explicit := lipapi.HasExplicitCompletion(items)
	assert.False(t, explicit, "call-only must be false")
	fv := &fakeVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop}}
	gate := New(Ports{Verifier: fv}, Config{
		Enabled:                  true,
		ExplicitCompletionPolicy: stopguard.PolicyVerify,
		MaxSemanticContinuations: 3,
		NoProgressLimit:          2,
	})
	cand := stopguard.Candidate{Cause: stopguard.CauseNormalEnd, ExplicitCompletion: explicit, OutputCommitted: true}
	facts := terminalFacts(cand)
	_ = gate.ObserveCandidate(context.Background(), facts)
	require.Equal(t, 1, fv.CallCount())
	ev, _ := fv.LastEvidence()
	assert.False(t, ev.ExplicitCompletion)
}
