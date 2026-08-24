package lipapi_test

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsExplicitCompletionToolName_Normalization(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"lowercase attempt_completion", "attempt_completion", true},
		{"case-fold Attempt_Completion", "Attempt_Completion", true},
		{"whitespace trimmed attempt_complete", "  attempt_complete  ", true},
		{"uppercase ATTEMPT_COMPLETE", "ATTEMPT_COMPLETE", true},
		{"generic finish rejected", "finish", false},
		{"task_complete rejected (conservative)", "task_complete", false},
		{"complete_task rejected", "complete_task", false},
		{"task_completion rejected", "task_completion", false},
		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"unknown tool", "read", false},
		{"prefix not inferred", "attempt_completion_extra", false},
		{"substring not inferred", "my_attempt_completion", false},
		{"done not in set", "done", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, lipapi.IsExplicitCompletionToolName(tc.input))
		})
	}
}

func TestIsExplicitCompletionItem_ValidAndMalformed(t *testing.T) {
	t.Parallel()
	valid := lipapi.Item{
		Kind: lipapi.ItemKindToolCall,
		ToolCall: &lipapi.ToolCallItem{
			CallID:    "call-1",
			Name:      "attempt_completion",
			Arguments: json.RawMessage(`{"result":"done"}`),
		},
	}
	assert.True(t, lipapi.IsExplicitCompletionItem(valid), "valid explicit name should be true (name-only predicate)")

	malformedCallID := lipapi.Item{
		Kind: lipapi.ItemKindToolCall,
		ToolCall: &lipapi.ToolCallItem{
			CallID: "   ",
			Name:   "attempt_completion",
		},
	}
	assert.False(t, lipapi.IsExplicitCompletionItem(malformedCallID), "missing call ID is malformed -> false")

	malformedArgs := lipapi.Item{
		Kind: lipapi.ItemKindToolCall,
		ToolCall: &lipapi.ToolCallItem{
			CallID:    "call-1",
			Name:      "attempt_completion",
			Arguments: json.RawMessage(`{invalid json`),
		},
	}
	assert.False(t, lipapi.IsExplicitCompletionItem(malformedArgs), "invalid JSON args is malformed -> false")

	unknownName := lipapi.Item{
		Kind:     lipapi.ItemKindToolCall,
		ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "read"},
	}
	assert.False(t, lipapi.IsExplicitCompletionItem(unknownName))

	wrongKind := lipapi.Item{
		Kind:     lipapi.ItemKindMessage,
		Role:     lipapi.RoleAssistant,
		ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "attempt_completion"},
	}
	assert.False(t, lipapi.IsExplicitCompletionItem(wrongKind))

	nilToolCall := lipapi.Item{Kind: lipapi.ItemKindToolCall}
	assert.False(t, lipapi.IsExplicitCompletionItem(nilToolCall))

	// Case-folded name should still be valid
	caseFolded := lipapi.Item{
		Kind:     lipapi.ItemKindToolCall,
		ToolCall: &lipapi.ToolCallItem{CallID: "c2", Name: "Attempt_Complete"},
	}
	assert.True(t, lipapi.IsExplicitCompletionItem(caseFolded))

	// Generic finish must be rejected (name-only predicate now false)
	finish := lipapi.Item{
		Kind:     lipapi.ItemKindToolCall,
		ToolCall: &lipapi.ToolCallItem{CallID: "c3", Name: "finish"},
	}
	assert.False(t, lipapi.IsExplicitCompletionItem(finish), "generic finish must not be explicit completion")
}

func TestHasExplicitCompletion_RequiresCorrelatedResult(t *testing.T) {
	t.Parallel()
	// Call only -> false (no executed evidence)
	callOnly := []lipapi.Item{
		{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusCompleted, ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "attempt_completion", Arguments: json.RawMessage(`{}`)}},
	}
	assert.False(t, lipapi.HasExplicitCompletion(callOnly), "call without matching result must be false")

	// Matching completed result -> true
	correlated := []lipapi.Item{
		{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusCompleted, ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "attempt_completion", Arguments: json.RawMessage(`{"result":"done"}`)}},
		{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: "c1", Name: "attempt_completion", Output: "ok"}},
	}
	assert.True(t, lipapi.HasExplicitCompletion(correlated), "correlated completed call+result must be true")

	// Orphan result (no matching call) -> false
	orphan := []lipapi.Item{
		{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: "c1", Name: "attempt_completion", Output: "ok"}},
	}
	assert.False(t, lipapi.HasExplicitCompletion(orphan), "orphan result must be false")

	// In-progress call -> false even with result
	inProgress := []lipapi.Item{
		{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusInProgress, ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "attempt_completion"}},
		{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: "c1", Name: "attempt_completion", Output: "ok"}},
	}
	assert.False(t, lipapi.HasExplicitCompletion(inProgress), "in-progress call must be false")

	// Incomplete result -> false
	incompleteResult := []lipapi.Item{
		{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusCompleted, ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "attempt_completion"}},
		{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusIncomplete, ToolResult: &lipapi.ToolResultItem{CallID: "c1", Name: "attempt_completion", Output: "ok"}},
	}
	assert.False(t, lipapi.HasExplicitCompletion(incompleteResult), "incomplete result must be false")

	// Mismatched CallID -> false
	mismatched := []lipapi.Item{
		{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusCompleted, ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "attempt_completion"}},
		{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: "c2", Name: "attempt_completion", Output: "ok"}},
	}
	assert.False(t, lipapi.HasExplicitCompletion(mismatched), "mismatched CallID must be false")

	// Generic finish correlation must still be false (conservative)
	finishCorrelated := []lipapi.Item{
		{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusCompleted, ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "finish"}},
		{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: "c1", Name: "finish", Output: "ok"}},
	}
	assert.False(t, lipapi.HasExplicitCompletion(finishCorrelated), "generic finish must be rejected even when correlated")

	assert.False(t, lipapi.HasExplicitCompletion(nil))
	assert.False(t, lipapi.HasExplicitCompletion([]lipapi.Item{}))
}

func TestHasExplicitCompletion_MalformedFallsBack(t *testing.T) {
	t.Parallel()
	malformed := []lipapi.Item{
		{Kind: lipapi.ItemKindToolCall, ToolCall: &lipapi.ToolCallItem{CallID: "", Name: "attempt_completion"}},
		{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: "", Name: "attempt_completion", Output: "ok"}},
	}
	assert.False(t, lipapi.HasExplicitCompletion(malformed), "malformed/absent explicit signal falls back to false")

	invalidJSON := []lipapi.Item{
		{Kind: lipapi.ItemKindToolCall, ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "attempt_completion", Arguments: json.RawMessage(`not json`)}},
		{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: "c1", Name: "attempt_completion", Output: "ok"}},
	}
	assert.False(t, lipapi.HasExplicitCompletion(invalidJSON))
}

func TestHasExplicitCompletion_VerifyPassesStrongEvidence(t *testing.T) {
	t.Parallel()
	correlated := []lipapi.Item{
		{Kind: lipapi.ItemKindToolCall, Status: lipapi.ItemStatusCompleted, ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "attempt_completion"}},
		{Kind: lipapi.ItemKindToolResult, Status: lipapi.ItemStatusCompleted, ToolResult: &lipapi.ToolResultItem{CallID: "c1", Name: "attempt_completion", Output: "done"}},
	}
	require.True(t, lipapi.HasExplicitCompletion(correlated))
}

func TestExplicitCompletion_NoProviderNamesInHelper(t *testing.T) {
	t.Parallel()
	providers := []string{"openai", "anthropic", "gemini", "bedrock", "openresponses", "claude"}
	for _, p := range providers {
		assert.False(t, lipapi.IsExplicitCompletionToolName(p), "provider name %q must not be treated as explicit completion", p)
	}
}
