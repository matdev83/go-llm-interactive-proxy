package stopguardverify_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguardverify"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func textMessage(role lipapi.Role, text string) lipapi.Message {
	return lipapi.Message{Role: role, Parts: []lipapi.Part{lipapi.TextPart(text)}}
}

func assistantTextItem(text string) lipapi.Item {
	return lipapi.Item{
		Kind:    lipapi.ItemKindMessage,
		Role:    lipapi.RoleAssistant,
		Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: text}},
		Status:  lipapi.ItemStatusCompleted,
	}
}

func toolCallItem(callID, name string, args json.RawMessage) lipapi.Item {
	return lipapi.Item{
		Kind:     lipapi.ItemKindToolCall,
		Status:   lipapi.ItemStatusCompleted,
		ToolCall: &lipapi.ToolCallItem{CallID: callID, Name: name, Arguments: args},
	}
}

func toolResultItem(callID, name, output string) lipapi.Item {
	return lipapi.Item{
		Kind:       lipapi.ItemKindToolResult,
		Status:     lipapi.ItemStatusCompleted,
		ToolResult: &lipapi.ToolResultItem{CallID: callID, Name: name, Output: output},
	}
}

func TestProjectEvidence_BoundedAndContainsSections(t *testing.T) {
	t.Parallel()
	ev := stopguard.Evidence{
		Cause:               stopguard.CauseNormalEnd,
		UserObjective:       []lipapi.Message{textMessage(lipapi.RoleUser, "Please fix the failing tests and ensure coverage.")},
		CandidateAssistant:  []lipapi.Item{assistantTextItem("Done; tests pass. Coverage is good.")},
		RecentTrajectory:    []lipapi.Item{toolCallItem("c1", "bash", json.RawMessage(`{"cmd":"go test"}`)), toolResultItem("c1", "bash", "PASS")},
		ToolState:           stopguard.ToolCompletionState{CompletedToolResults: 1},
		OutputCommitted:     true,
		ExplicitCompletion:  false,
		ContinuationLineage: stopguard.ContinuationRef{ContinuationID: "cont-123"},
		RecoveryAttempt:     1,
		ParentTraceID:       "trace-1",
		ParentALegID:        "a-1",
		ParentBLegID:        "b-1",
		ParentBranchBinding: "branch-1",
	}
	block := stopguardverify.ProjectEvidence(ev)
	require.NotEmpty(t, block)
	assert.Contains(t, block, "Cause: normal_end")
	assert.Contains(t, block, "UserObjective:")
	assert.Contains(t, block, "fix the failing tests")
	assert.Contains(t, block, "CandidateAssistant:")
	assert.Contains(t, block, "Done; tests pass")
	assert.Contains(t, block, "RecentTrajectory:")
	assert.Contains(t, block, "tool_call name=bash")
	assert.Contains(t, block, "tool_result name=bash")
	assert.Contains(t, block, "RecoveryAttempt: 1")
	assert.Contains(t, block, "ContinuationID: cont-123")
	assert.Contains(t, block, "OutputCommitted: true")
	assert.LessOrEqual(t, len(block), stopguardverify.MaxEvidenceBytes)
	// No raw args payload beyond digest.
	assert.NotContains(t, block, `"cmd":"go test"`)
	assert.Contains(t, block, "args_sha256:")
}

func TestProjectEvidence_ToolSummaryOnlyNameStatusNoRawPayloads(t *testing.T) {
	t.Parallel()
	ev := stopguard.Evidence{
		Cause:              stopguard.CauseNormalEnd,
		UserObjective:      []lipapi.Message{textMessage(lipapi.RoleUser, "do work")},
		CandidateAssistant: []lipapi.Item{toolCallItem("c2", "write", json.RawMessage(`{"path":"/tmp/secret","content":"huge payload that should not appear raw"}`))},
		RecentTrajectory:   []lipapi.Item{toolCallItem("c2", "write", json.RawMessage(`{"path":"/tmp/secret"}`))},
		ToolState:          stopguard.ToolCompletionState{CompletedToolResults: 0, HasIncompleteArguments: false},
	}
	block := stopguardverify.ProjectEvidence(ev)
	assert.NotContains(t, block, "huge payload")
	assert.NotContains(t, block, "/tmp/secret")
	assert.Contains(t, block, "tool_call name=write")
	assert.Contains(t, block, "args_sha256:")
}

func TestProjectEvidence_BoundsTruncation(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 5000)
	ev := stopguard.Evidence{
		Cause:              stopguard.CauseNormalEnd,
		UserObjective:      []lipapi.Message{textMessage(lipapi.RoleUser, long)},
		CandidateAssistant: []lipapi.Item{assistantTextItem(long + long)},
		RecentTrajectory:   make([]lipapi.Item, 0),
	}
	block := stopguardverify.ProjectEvidence(ev)
	assert.LessOrEqual(t, len(block), stopguardverify.MaxEvidenceBytes)
	// The objective text should be bounded inside block (contains truncation marker or just cut).
	assert.LessOrEqual(t, len([]byte(block)), stopguardverify.MaxEvidenceBytes)
}

func TestProjectEvidence_EmptySlicesNoPanic(t *testing.T) {
	t.Parallel()
	ev := stopguard.Evidence{Cause: stopguard.CauseNormalEnd}
	block := stopguardverify.ProjectEvidence(ev)
	assert.Contains(t, block, "(none)")
	assert.LessOrEqual(t, len(block), stopguardverify.MaxEvidenceBytes)
}

func TestProjectEvidence_PureFunctionNoSecondStore(t *testing.T) {
	t.Parallel()
	ev := stopguard.Evidence{
		UserObjective:      []lipapi.Message{textMessage(lipapi.RoleUser, "original objective")},
		CandidateAssistant: []lipapi.Item{assistantTextItem("candidate")},
	}
	block1 := stopguardverify.ProjectEvidence(ev)
	// Mutate input slices after projection; projection must not retain references.
	ev.UserObjective[0].Parts[0].Text = "mutated"
	ev.CandidateAssistant[0].Content[0].Text = "mutated2"
	block2 := stopguardverify.ProjectEvidence(ev)
	assert.Contains(t, block1, "original objective")
	assert.Contains(t, block1, "candidate")
	assert.Contains(t, block2, "mutated")
	// Ensure first block unchanged.
	assert.NotEqual(t, block1, block2)
}

func TestProjectEvidence_ContinuationLineageAndAttempt(t *testing.T) {
	t.Parallel()
	ev := stopguard.Evidence{
		Cause:               stopguard.CauseNormalEnd,
		ContinuationLineage: stopguard.ContinuationRef{ContinuationID: "lineage-xyz"},
		RecoveryAttempt:     2,
		ExplicitCompletion:  true,
		OutputCommitted:     true,
	}
	block := stopguardverify.ProjectEvidence(ev)
	assert.Contains(t, block, "ContinuationID: lineage-xyz")
	assert.Contains(t, block, "RecoveryAttempt: 2")
	assert.Contains(t, block, "ExplicitCompletion: true")
}
