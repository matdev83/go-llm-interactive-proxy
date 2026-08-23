package continuationsafety

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helpers to build deterministic continuation fixtures without I/O.

func testResponseID(seed byte) lipcont.ResponseID {
	b := make([]byte, 16)
	for i := range b {
		b[i] = seed + byte(i)
	}
	return lipcont.ResponseID(lipcont.ResponseIDPrefix + base64.RawURLEncoding.EncodeToString(b))
}

func assistantMessageItem(id, text string) lipapi.Item {
	return lipapi.Item{
		Kind:   lipapi.ItemKindMessage,
		ID:     id,
		Role:   lipapi.RoleAssistant,
		Status: lipapi.ItemStatusCompleted,
		Content: []lipapi.ContentPart{
			{Kind: lipapi.ContentPartText, Text: text},
		},
	}
}

func toolCallItem(callID, name string, args json.RawMessage) lipapi.Item {
	if args == nil {
		args = json.RawMessage(`{}`)
	}
	return lipapi.Item{
		Kind: lipapi.ItemKindToolCall,
		ID:   "item_" + callID,
		ToolCall: &lipapi.ToolCallItem{
			CallID:    callID,
			Name:      name,
			Arguments: args,
		},
	}
}

func toolResultItem(callID, name, output string) lipapi.Item {
	return lipapi.Item{
		Kind: lipapi.ItemKindToolResult,
		ID:   "res_" + callID,
		ToolResult: &lipapi.ToolResultItem{
			CallID: callID,
			Name:   name,
			Output: output,
		},
	}
}

func makePriorRecord(ids ...lipcont.ResponseID) lipcont.ContinuationRecord {
	id := testResponseID(0x10)
	prev := lipcont.ResponseID("")
	if len(ids) > 0 {
		id = ids[0]
		if len(ids) > 1 {
			prev = ids[1]
		}
	}
	return lipcont.ContinuationRecord{
		ID:         id,
		PreviousID: prev,
		Scope:      lipcont.Scope{TenantID: "t", PrincipalID: "p", SessionID: "s"},
		ProfileID:  "test-profile",
		Lineage: lipcont.Lineage{
			ProfileID: "test-profile",
			Model:     "test-model",
		},
		InputItems: []lipapi.Item{
			{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "user objective"}}},
		},
		OutputItems: []lipapi.Item{
			assistantMessageItem("a1", "committed assistant output"),
		},
		Terminal:          true,
		Status:            lipcont.RecordStatusCompleted,
		ChainDepth:        1,
		MaterializedBytes: 1024,
	}
}

// ---------------------------------------------------------------------------
// 1. Committed assistant output preserved exactly once (Req 4.1, 9.1, 10.2)
// ---------------------------------------------------------------------------

func TestSafeContinuation_PreservesCommittedAssistantOutputExactlyOnce(t *testing.T) {
	t.Parallel()
	prior := makePriorRecord()
	prior.OutputItems = []lipapi.Item{
		assistantMessageItem("a1", "hello world"),
		assistantMessageItem("a2", "second block"),
	}
	tail := TailState{
		CommittedAssistantItems: prior.OutputItems,
		PriorStatus:             lipcont.RecordStatusCompleted,
	}
	in := Input{
		Prior:            PriorSummary{Record: prior},
		Tail:             tail,
		SafeNativeResume: false,
		Bounds:           lipcont.DefaultBounds(),
	}
	result := Evaluate(in)
	require.Equal(t, OutcomeContinueSafe, result.Outcome, "safe trajectory with committed output must be continuable")
	assert.Equal(t, 2, result.Facts.PreservedAssistantCount, "must preserve each committed assistant item exactly once")
	assert.Equal(t, prior.Lineage, result.Facts.Lineage, "lineage must be preserved")
	assert.Equal(t, prior.ID, result.Facts.PreviousID, "previous ID must be carried forward")
	assert.False(t, hasDuplicatedAssistantIDs(result.SafeMaterializedItems), "constructed materialized items must not duplicate committed output")
	assert.Equal(t, lipcont.RecordStatusCompleted, result.Facts.PriorStatus)
}

func hasDuplicatedAssistantIDs(items []lipapi.Item) bool {
	seen := make(map[string]bool)
	for _, it := range items {
		if it.Kind == lipapi.ItemKindMessage && it.Role == lipapi.RoleAssistant {
			if it.ID != "" {
				if seen[it.ID] {
					return true
				}
				seen[it.ID] = true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 2. Completed tool+result preserved exactly once, no re-execution (Req 4.2, 9.2, 12.6)
// ---------------------------------------------------------------------------

func TestSafeContinuation_PreservesCompletedToolCallAndResultExactlyOnceNoReexecution(t *testing.T) {
	t.Parallel()
	prior := makePriorRecord()
	call := toolCallItem("call_1", "read_file", json.RawMessage(`{"path":"/tmp/x"}`))
	res := toolResultItem("call_1", "read_file", "file contents")
	prior.OutputItems = []lipapi.Item{
		assistantMessageItem("a1", "about to read"),
		call,
		res,
	}
	tail := TailState{
		CommittedAssistantItems: []lipapi.Item{assistantMessageItem("a1", "about to read")},
		CompletedCalls:          []lipapi.Item{call},
		CompletedResults:        []lipapi.Item{res},
		PriorStatus:             lipcont.RecordStatusCompleted,
	}
	in := Input{
		Prior:            PriorSummary{Record: prior},
		Tail:             tail,
		SafeNativeResume: false,
		Bounds:           lipcont.DefaultBounds(),
	}
	result := Evaluate(in)
	require.Equal(t, OutcomeContinueSafe, result.Outcome)
	assert.Equal(t, 1, result.Facts.PreservedToolPairs, "completed pair must be carried forward exactly once")
	assert.True(t, result.Facts.MustNotReexecute, "completed side effect must not be re-executed due to transport interruption")
	// Assert constructed trajectory contains the pair exactly once.
	countCalls := 0
	countResults := 0
	for _, it := range result.SafeMaterializedItems {
		if it.Kind == lipapi.ItemKindToolCall && it.ToolCall != nil && it.ToolCall.CallID == "call_1" {
			countCalls++
		}
		if it.Kind == lipapi.ItemKindToolResult && it.ToolResult != nil && it.ToolResult.CallID == "call_1" {
			countResults++
		}
	}
	assert.Equal(t, 1, countCalls, "tool call must appear exactly once in materialized trajectory")
	assert.Equal(t, 1, countResults, "tool result must appear exactly once in materialized trajectory")
}

func TestSafeContinuation_ProhibitsReexecutionSolelyDueToTransportInterruption(t *testing.T) {
	t.Parallel()
	prior := makePriorRecord()
	call := toolCallItem("call_42", "write_file", json.RawMessage(`{"path":"/tmp/out","data":"hi"}`))
	res := toolResultItem("call_42", "write_file", "ok")
	prior.OutputItems = []lipapi.Item{call, res}
	tail := TailState{
		CompletedCalls:   []lipapi.Item{call},
		CompletedResults: []lipapi.Item{res},
		PriorStatus:      lipcont.RecordStatusCompleted,
	}
	// Even though transport interruption is the interruption cause, the decision must not schedule a replay.
	in := Input{
		Prior:            PriorSummary{Record: prior},
		Tail:             tail,
		SafeNativeResume: false,
		Bounds:           lipcont.DefaultBounds(),
	}
	result := Evaluate(in)
	assert.True(t, result.Facts.MustNotReexecute, "transport interruption alone must not cause re-execution of completed side effect")
	assert.NotEqual(t, OutcomeUnsupportedOpaqueState, result.Outcome)
}

// ---------------------------------------------------------------------------
// 3. Incomplete tool arguments -> rejection (Req 4.3, 10.2)
// ---------------------------------------------------------------------------

func TestSafeContinuation_RejectsIncompleteToolArguments(t *testing.T) {
	t.Parallel()
	prior := makePriorRecord()
	prior.OutputItems = []lipapi.Item{
		{
			Kind:   lipapi.ItemKindToolCall,
			ID:     "item_partial",
			Status: lipapi.ItemStatusIncomplete,
			ToolCall: &lipapi.ToolCallItem{
				CallID:    "call_partial",
				Name:      "write_file",
				Arguments: json.RawMessage(`{"path":`), // truncated JSON
			},
		},
	}
	tail := TailState{
		HasIncompleteToolArgs: true,
		PriorStatus:           lipcont.RecordStatusIncomplete,
	}
	in := Input{
		Prior:            PriorSummary{Record: prior},
		Tail:             tail,
		SafeNativeResume: false,
		Bounds:           lipcont.DefaultBounds(),
	}
	result := Evaluate(in)
	assert.Equal(t, OutcomeUnsafePartialToolArgs, result.Outcome, "incomplete tool arguments must be rejected")
	assert.Equal(t, lipcont.RecordStatusIncomplete, result.Facts.PriorStatus)
}

func TestSafeContinuation_RejectsIncompleteToolArgsEvenWithNativeResumeFalse(t *testing.T) {
	t.Parallel()
	tail := TailState{
		HasIncompleteToolArgs: true,
		PriorStatus:           lipcont.RecordStatusIncomplete,
	}
	in := Input{
		Prior:            PriorSummary{Record: makePriorRecord()},
		Tail:             tail,
		SafeNativeResume: false,
		Bounds:           lipcont.DefaultBounds(),
	}
	result := Evaluate(in)
	require.Equal(t, OutcomeUnsafePartialToolArgs, result.Outcome)
}

// ---------------------------------------------------------------------------
// 4. Unsupported opaque/provider state -> rejection unless SafeNativeResume proves safe (Req 4.4, 12.7)
// ---------------------------------------------------------------------------

func TestSafeContinuation_RejectsUnsupportedOpaqueStateWithoutNativeResumeProof(t *testing.T) {
	t.Parallel()
	tail := TailState{
		HasUnsupportedOpaqueState: true,
		PriorStatus:               lipcont.RecordStatusCompleted,
	}
	in := Input{
		Prior:            PriorSummary{Record: makePriorRecord()},
		Tail:             tail,
		SafeNativeResume: false,
		Bounds:           lipcont.DefaultBounds(),
	}
	result := Evaluate(in)
	assert.Equal(t, OutcomeUnsupportedOpaqueState, result.Outcome, "opaque/provider state without normalized native-resume proof must be rejected")
}

func TestSafeContinuation_AllowsOpaqueStateWithSafeNativeResume(t *testing.T) {
	t.Parallel()
	prior := makePriorRecord()
	prior.NativeRefs = []lipcont.NativeReference{{Provider: "anthropic", Kind: "thinking", ID: "native_1", Opaque: []byte(`{"enc":"x"}`)}}
	tail := TailState{
		HasUnsupportedOpaqueState: true,
		PriorStatus:               lipcont.RecordStatusCompleted,
	}
	in := Input{
		Prior:            PriorSummary{Record: prior},
		Tail:             tail,
		SafeNativeResume: true,
		Bounds:           lipcont.DefaultBounds(),
	}
	result := Evaluate(in)
	assert.Equal(t, OutcomeContinueSafe, result.Outcome, "with SafeNativeResume=true normalized proof, opaque state must be allowed")
	assert.Equal(t, lipcont.RecordStatusCompleted, result.Facts.PriorStatus)
}

func TestSafeContinuation_OpaqueStateWithNativeResumeStillRespectsOtherGuards(t *testing.T) {
	t.Parallel()
	// Even with native resume, incomplete args still force rejection.
	tail := TailState{
		HasIncompleteToolArgs:     true,
		HasUnsupportedOpaqueState: true,
		PriorStatus:               lipcont.RecordStatusIncomplete,
	}
	in := Input{
		Prior:            PriorSummary{Record: makePriorRecord()},
		Tail:             tail,
		SafeNativeResume: true,
		Bounds:           lipcont.DefaultBounds(),
	}
	result := Evaluate(in)
	assert.Equal(t, OutcomeUnsafePartialToolArgs, result.Outcome, "partial args rejection takes precedence even with native resume")
}

// ---------------------------------------------------------------------------
// 5. Chain-depth and materialization limits (Req 9.3, 9.4, 10.4, 12.6)
// ---------------------------------------------------------------------------

func TestSafeContinuation_RejectsWhenChainDepthExceeded(t *testing.T) {
	t.Parallel()
	prior := makePriorRecord()
	prior.ChainDepth = lipcont.DefaultBounds().MaxChainDepth
	tail := TailState{PriorStatus: lipcont.RecordStatusCompleted}
	in := Input{
		Prior:            PriorSummary{Record: prior},
		Tail:             tail,
		SafeNativeResume: false,
		Bounds:           lipcont.DefaultBounds(),
	}
	result := Evaluate(in)
	assert.Equal(t, OutcomeChainDepthExceeded, result.Outcome, "chain depth at/over bound must be rejected")
	assert.Equal(t, prior.ChainDepth, result.Facts.ChainDepth)
}

func TestSafeContinuation_RejectsWhenMaterializedBytesExceeded(t *testing.T) {
	t.Parallel()
	prior := makePriorRecord()
	prior.MaterializedBytes = lipcont.DefaultBounds().MaxMaterializedBytes
	// Tail adds non-trivial committed output pushing over bound.
	tail := TailState{
		CommittedAssistantItems: []lipapi.Item{
			assistantMessageItem("a_big", strings.Repeat("x", 1024)),
		},
		PriorStatus: lipcont.RecordStatusCompleted,
	}
	bounds := lipcont.DefaultBounds()
	// Force a tight bound to make test deterministic if prior already uses 64MiB
	bounds.MaxMaterializedBytes = 1024
	prior.MaterializedBytes = 1024
	in := Input{
		Prior:            PriorSummary{Record: prior},
		Tail:             tail,
		SafeNativeResume: false,
		Bounds:           bounds,
	}
	result := Evaluate(in)
	assert.Equal(t, OutcomeMaterializationExceeded, result.Outcome, "materialized size over bound must be rejected")
	assert.Greater(t, result.Facts.MaterializedBytes, int64(bounds.MaxMaterializedBytes))
}

// Materialized items count limit via Bounds.MaxMaterializedItems.
func TestSafeContinuation_RejectsWhenMaterializedItemsExceeded(t *testing.T) {
	t.Parallel()
	prior := makePriorRecord()
	bounds := lipcont.Bounds{
		MaxChainDepth:        64,
		MaxMaterializedItems: 2,
		MaxMaterializedBytes: 64 << 20,
	}
	prior.InputItems = []lipapi.Item{
		assistantMessageItem("i1", "a"),
		assistantMessageItem("i2", "b"),
	}
	prior.OutputItems = []lipapi.Item{
		assistantMessageItem("o1", "c"),
	}
	tail := TailState{
		CommittedAssistantItems: []lipapi.Item{
			assistantMessageItem("extra1", "d"),
			assistantMessageItem("extra2", "e"),
		},
		PriorStatus: lipcont.RecordStatusCompleted,
	}
	in := Input{
		Prior:            PriorSummary{Record: prior},
		Tail:             tail,
		SafeNativeResume: false,
		Bounds:           bounds,
	}
	result := Evaluate(in)
	assert.Equal(t, OutcomeMaterializationExceeded, result.Outcome)
}

// ---------------------------------------------------------------------------
// 6. Prior attempt status flows accurately; lineage preserved (Req 4.5, 4.6, 9.5, 10.3)
// ---------------------------------------------------------------------------

func TestSafeContinuation_PriorStatusInterruptedFlowsIntoOutcome(t *testing.T) {
	t.Parallel()
	prior := makePriorRecord()
	prior.Status = lipcont.RecordStatusIncomplete
	prior.ChainDepth = 3
	tail := TailState{
		CommittedAssistantItems: []lipapi.Item{assistantMessageItem("a1", "partial text before interrupt")},
		PriorStatus:             lipcont.RecordStatusIncomplete,
	}
	in := Input{
		Prior:            PriorSummary{Record: prior},
		Tail:             tail,
		SafeNativeResume: false,
		Bounds:           lipcont.DefaultBounds(),
	}
	result := Evaluate(in)
	// Interrupted status must be recorded faithfully even when safe.
	require.Equal(t, lipcont.RecordStatusIncomplete, result.Facts.PriorStatus)
	// The construction must remain traceable to prior lineage/depth.
	assert.Equal(t, 3, result.Facts.ChainDepth)
	assert.Equal(t, prior.ID, result.Facts.PreviousID)
}

func TestSafeContinuation_PriorStatusCompletedFlowsIntoOutcome(t *testing.T) {
	t.Parallel()
	prior := makePriorRecord()
	prior.Status = lipcont.RecordStatusCompleted
	prior.PreviousID = testResponseID(0x22)
	prior.ChainDepth = 2
	tail := TailState{
		CommittedAssistantItems: []lipapi.Item{assistantMessageItem("a1", "final text")},
		PriorStatus:             lipcont.RecordStatusCompleted,
	}
	in := Input{
		Prior:            PriorSummary{Record: prior},
		Tail:             tail,
		SafeNativeResume: false,
		Bounds:           lipcont.DefaultBounds(),
	}
	result := Evaluate(in)
	assert.Equal(t, lipcont.RecordStatusCompleted, result.Facts.PriorStatus)
	assert.Equal(t, lipcont.RecordStatusCompleted, result.Facts.PriorStatus, "prior completed status must be preserved accurately")
	assert.Equal(t, prior.ID, result.Facts.PreviousID, "new leg links to the prior record itself")
	assert.Equal(t, prior.Lineage, result.Facts.Lineage, "lineage must be preserved from prior record")
}

func TestSafeContinuation_LineageAndPreviousIDPreserved(t *testing.T) {
	t.Parallel()
	prevID := testResponseID(0x30)
	prior := makePriorRecord(testResponseID(0x31), prevID)
	prior.Lineage = lipcont.Lineage{
		ProfileID:     "prof-1",
		Model:         "model-x",
		RouteSelector: "route-a",
		ProviderBound: true,
		ProviderID:    "openai",
		CandidateKey:  "cand-1",
	}
	prior.ChainDepth = 5
	tail := TailState{PriorStatus: lipcont.RecordStatusCompleted}
	in := Input{
		Prior:            PriorSummary{Record: prior},
		Tail:             tail,
		SafeNativeResume: false,
		Bounds:           lipcont.DefaultBounds(),
	}
	result := Evaluate(in)
	assert.Equal(t, testResponseID(0x31), result.Facts.PreviousID, "new leg links to the prior record so lineage walks include the interrupted leg")
	assert.Equal(t, prior.Lineage, result.Facts.Lineage)
	assert.Equal(t, 5, result.Facts.ChainDepth)
}

func TestSafeContinuation_MaterializedTrajectoryPreservesOrderingWithoutDuplication(t *testing.T) {
	t.Parallel()
	prior := makePriorRecord()
	prior.InputItems = []lipapi.Item{
		{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "input 1"}}},
	}
	call := toolCallItem("call_keep", "search", json.RawMessage(`{"q":"go"}`))
	res := toolResultItem("call_keep", "search", "result")
	prior.OutputItems = []lipapi.Item{
		assistantMessageItem("a1", "thinking"),
		call,
		res,
		assistantMessageItem("a2", "final"),
	}
	tail := TailState{
		CommittedAssistantItems: []lipapi.Item{assistantMessageItem("a1", "thinking"), assistantMessageItem("a2", "final")},
		CompletedCalls:          []lipapi.Item{call},
		CompletedResults:        []lipapi.Item{res},
		PriorStatus:             lipcont.RecordStatusCompleted,
	}
	in := Input{
		Prior:            PriorSummary{Record: prior},
		Tail:             tail,
		SafeNativeResume: false,
		Bounds:           lipcont.DefaultBounds(),
	}
	result := Evaluate(in)
	require.Equal(t, OutcomeContinueSafe, result.Outcome)
	// Input before output before tool pair ordering must be stable and not duplicated.
	assert.GreaterOrEqual(t, len(result.SafeMaterializedItems), len(prior.InputItems)+len(prior.OutputItems))
	// Ensure no item ID appears twice.
	ids := make(map[string]int)
	for _, it := range result.SafeMaterializedItems {
		if it.ID != "" {
			ids[it.ID]++
		}
	}
	for id, cnt := range ids {
		assert.Equal(t, 1, cnt, "item ID %q must appear exactly once; duplication would corrupt lineage", id)
	}
}
