package lipapi_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestProjectItemsToLegacyView_portableMessageTrajectory(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Items: []lipapi.Item{
			{
				Kind:   lipapi.ItemKindMessage,
				ID:     "inst-0",
				Status: lipapi.ItemStatusCompleted,
				Role:   lipapi.RoleSystem,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "You are helpful."},
				},
			},
			{
				Kind:   lipapi.ItemKindMessage,
				ID:     "msg-1",
				Status: lipapi.ItemStatusCompleted,
				Role:   lipapi.RoleUser,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "Hello"},
				},
			},
			{
				Kind:   lipapi.ItemKindToolCall,
				ID:     "call-1",
				Status: lipapi.ItemStatusCompleted,
				ToolCall: &lipapi.ToolCallItem{
					CallID:    "call_abc",
					Name:      "weather",
					Arguments: json.RawMessage(`{"city":"SF"}`),
				},
			},
			{
				Kind:   lipapi.ItemKindToolResult,
				ID:     "result-1",
				Status: lipapi.ItemStatusCompleted,
				ToolResult: &lipapi.ToolResultItem{
					CallID: "call_abc",
					Name:   "weather",
					Output: "72F",
				},
			},
		},
	}
	target := lipapi.DefaultLegacyProjectionTarget(
		lipapi.NewBackendCaps(lipapi.CapabilityTools, lipapi.CapabilityStreaming),
		lipapi.ReasoningReplaySupport{},
	)
	got, err := lipapi.ProjectItemsToLegacyView(call, target)
	if err != nil {
		t.Fatalf("ProjectItemsToLegacyView: %v", err)
	}
	if len(got.Instructions) != 1 || got.Instructions[0].Role != lipapi.RoleSystem {
		t.Fatalf("instructions=%#v", got.Instructions)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("messages len=%d", len(got.Messages))
	}
	if got.Messages[1].Role != lipapi.RoleAssistant || got.Messages[1].Parts[0].ToolCallID != "call_abc" {
		t.Fatalf("tool call projection=%#v", got.Messages[1])
	}
	if got.Messages[2].Role != lipapi.RoleTool {
		t.Fatalf("tool result projection=%#v", got.Messages[2])
	}
}

func TestProjectItemsToLegacyView_rejectsPhaseBeforeBackend(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Items: []lipapi.Item{
			{
				Kind:   lipapi.ItemKindMessage,
				ID:     "msg-1",
				Status: lipapi.ItemStatusCompleted,
				Role:   lipapi.RoleAssistant,
				Phase:  lipapi.AssistantPhaseCommentary,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "thinking"},
				},
			},
		},
	}
	target := lipapi.DefaultLegacyProjectionTarget(lipapi.NewBackendCaps(lipapi.CapabilityStreaming), lipapi.ReasoningReplaySupport{})
	_, err := lipapi.ProjectItemsToLegacyView(call, target)
	if !lipapi.IsProjectionError(err) {
		t.Fatalf("expected projection error, got %v", err)
	}
	var pe *lipapi.ProjectionError
	if !errors.As(err, &pe) || pe.Reason != lipapi.ProjectionReasonAssistantPhase {
		t.Fatalf("got %#v", err)
	}
}

func TestProjectItemsToLegacyView_rejectsPhaseDespiteCapabilityFlag(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Items: []lipapi.Item{{
			Kind:   lipapi.ItemKindMessage,
			ID:     "msg-1",
			Status: lipapi.ItemStatusCompleted,
			Role:   lipapi.RoleAssistant,
			Phase:  lipapi.AssistantPhaseFinalAnswer,
			Content: []lipapi.ContentPart{
				{Kind: lipapi.ContentPartText, Text: "answer"},
			},
		}},
	}
	target := lipapi.LegacyProjectionTargetFromCaps(
		lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityAssistantPhase),
		lipapi.ReasoningReplaySupport{},
	)
	if target.SupportsPhase {
		t.Fatal("legacy projection must not advertise phase support")
	}
	_, err := lipapi.ProjectItemsToLegacyView(call, target)
	if !lipapi.IsProjectionError(err) {
		t.Fatalf("expected projection error, got %v", err)
	}
}

func TestProjectItemsToLegacyView_rejectsItemReferenceNoNetwork(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Items: []lipapi.Item{
			{
				Kind:      lipapi.ItemKindItemReference,
				ID:        "ref-1",
				Status:    lipapi.ItemStatusCompleted,
				Reference: &lipapi.ItemReference{ID: "msg-prev"},
			},
		},
	}
	target := lipapi.DefaultLegacyProjectionTarget(lipapi.NewBackendCaps(lipapi.CapabilityStreaming), lipapi.ReasoningReplaySupport{})
	_, err := lipapi.ProjectItemsToLegacyView(call, target)
	if !lipapi.IsProjectionError(err) {
		t.Fatalf("expected projection error, got %v", err)
	}
}

func TestProjectLegacyToOrderedItems_preservesToolFlow(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}},
			{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{{
					Kind:       lipapi.PartJSON,
					ToolCallID: "call_1",
					ToolName:   "weather",
					Content:    json.RawMessage(`{"city":"SF"}`),
				}},
			},
			{
				Role: lipapi.RoleTool,
				Parts: []lipapi.Part{{
					Kind:       lipapi.PartToolResult,
					ToolCallID: "call_1",
					ToolName:   "weather",
					Text:       "72F",
				}},
			},
		},
	}
	items, req, err := lipapi.ProjectLegacyToOrderedItems(call, lipapi.DefaultOrderedItemProjectionTarget())
	if err != nil {
		t.Fatalf("ProjectLegacyToOrderedItems: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items=%#v", items)
	}
	if items[1].Kind != lipapi.ItemKindToolCall || items[1].ToolCall.CallID != "call_1" {
		t.Fatalf("tool call item=%#v", items[1])
	}
	if items[2].Kind != lipapi.ItemKindToolResult {
		t.Fatalf("tool result item=%#v", items[2])
	}
	if !containsCapability(req.Capabilities, lipapi.CapabilityTools) {
		t.Fatalf("requirements=%#v", req)
	}
}

func TestProjectLegacyToOrderedItems_rejectsConflictingAuthority(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Items:    []lipapi.Item{{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "x"}}}},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("y")}}},
	}
	_, _, err := lipapi.ProjectLegacyToOrderedItems(call, lipapi.DefaultOrderedItemProjectionTarget())
	if err == nil {
		t.Fatal("expected error for conflicting authority")
	}
}

func TestDeriveProtocolRequirements_reasoningDialectAuthority(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleAssistant,
			Parts: []lipapi.Part{{
				Kind: lipapi.PartReasoning,
				Reasoning: &lipapi.ReasoningPart{
					Dialect: lipapi.ReasoningDialectOpenAIChatTextV1,
					Text:    "thought",
				},
			}},
		}},
	}
	req := lipapi.DeriveProtocolRequirements(call)
	if len(req.ReasoningDialects) != 1 || req.ReasoningDialects[0].Dialect != string(lipapi.ReasoningDialectOpenAIChatTextV1) {
		t.Fatalf("reasoning dialects=%#v", req.ReasoningDialects)
	}
	if !containsCapability(req.Capabilities, lipapi.CapabilityReasoningReplay) {
		t.Fatalf("caps=%#v", req.Capabilities)
	}
}

func TestMatchRequirements_rejectsMissingDialect(t *testing.T) {
	t.Parallel()

	required := lipapi.ProtocolRequirements{
		ReasoningDialects: []lipapi.DialectRequirement{{
			Kind:    "reasoning",
			Dialect: string(lipapi.ReasoningDialectOpenAIChatTextV1),
		}},
	}
	res := lipapi.MatchRequirements(required, lipapi.ProtocolRequirements{}, lipapi.ReasoningReplaySupport{})
	if res.Kind != lipapi.NegotiationReject {
		t.Fatalf("got %s", res.Kind)
	}
}

func TestAdmitCandidate_acceptsNativeItemReferenceBeforeNetwork(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Items: []lipapi.Item{
			{
				Kind:      lipapi.ItemKindItemReference,
				ID:        "ref-1",
				Status:    lipapi.ItemStatusCompleted,
				Reference: &lipapi.ItemReference{ID: "msg-prev"},
			},
		},
	}
	res := lipapi.AdmitCandidate(lipapi.CandidateAdmissionInput{
		Call: call,
		BackendCaps: lipapi.NewBackendCaps(
			lipapi.CapabilityStreaming,
			lipapi.CapabilityOrderedItems,
			lipapi.CapabilityItemReferences,
		),
		DialectSupport: lipapi.DialectSupport{
			ItemDialects: []lipapi.DialectRequirement{{Kind: "item", Dialect: "item_reference"}},
		},
		ProjectionTarget: lipapi.DefaultLegacyProjectionTarget(
			lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityOrderedItems),
			lipapi.ReasoningReplaySupport{},
		),
	})
	if res.Kind != lipapi.NegotiationLossless {
		t.Fatalf("got %s err=%v", res.Kind, res.Err())
	}
}

func TestRequiresProjectionAdaptation_authorityAware(t *testing.T) {
	t.Parallel()

	itemCall := lipapi.Call{Items: []lipapi.Item{{Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "hi"}}}}}
	legacyCall := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}}}
	ordered := lipapi.NewBackendCaps(lipapi.CapabilityOrderedItems)
	legacy := lipapi.NewBackendCaps(lipapi.CapabilityStreaming)

	if lipapi.RequiresProjectionAdaptation(itemCall, ordered) {
		t.Fatal("native ordered backend should not require projection for item authority")
	}
	if !lipapi.RequiresProjectionAdaptation(itemCall, legacy) {
		t.Fatal("legacy backend should require projection for item authority")
	}
	if !lipapi.RequiresProjectionAdaptation(legacyCall, ordered) {
		t.Fatal("ordered backend should require projection for legacy authority")
	}
	if lipapi.RequiresProjectionAdaptation(legacyCall, legacy) {
		t.Fatal("legacy backend should not require projection for legacy authority")
	}
}

func containsCapability(caps []lipapi.Capability, want lipapi.Capability) bool {
	return slices.Contains(caps, want)
}

func TestProjectItemsToLegacyView_sourceNonMutation(t *testing.T) {
	t.Parallel()

	origItem := lipapi.Item{
		Kind:   lipapi.ItemKindMessage,
		ID:     "msg-1",
		Status: lipapi.ItemStatusCompleted,
		Role:   lipapi.RoleUser,
		Content: []lipapi.ContentPart{
			{Kind: lipapi.ContentPartText, Text: "hello"},
		},
	}
	call := lipapi.Call{
		Items: []lipapi.Item{origItem},
	}
	target := lipapi.DefaultLegacyProjectionTarget(
		lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		lipapi.ReasoningReplaySupport{},
	)
	res, err := lipapi.ProjectItemsToLegacyView(call, target)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("expected 1 projected message, got %d", len(res.Messages))
	}
	// Verify source call was not mutated
	if len(call.Items) != 1 || call.Items[0].ID != "msg-1" || call.Items[0].Content[0].Text != "hello" {
		t.Fatalf("source Call was mutated: %#v", call)
	}
}

func TestProjectLegacyToOrderedItems_sourceNonMutation(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Instructions: []lipapi.Message{
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("system prompt")}},
		},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("user prompt")}},
		},
	}
	items, _, err := lipapi.ProjectLegacyToOrderedItems(call, lipapi.DefaultOrderedItemProjectionTarget())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 projected items, got %d", len(items))
	}
	// Verify source call was not mutated
	if len(call.Instructions) != 1 || call.Instructions[0].Parts[0].Text != "system prompt" {
		t.Fatalf("source Instructions were mutated: %#v", call.Instructions)
	}
	if len(call.Messages) != 1 || call.Messages[0].Parts[0].Text != "user prompt" {
		t.Fatalf("source Messages were mutated: %#v", call.Messages)
	}
}

func TestProjectItemsToLegacyView_deterministicRejectionReasonsTable(t *testing.T) {
	t.Parallel()

	defaultCaps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
	noReplay := lipapi.ReasoningReplaySupport{}

	tests := []struct {
		name       string
		call       lipapi.Call
		target     lipapi.LegacyProjectionTarget
		wantReason lipapi.ProjectionReason
	}{
		{
			name: "ConflictingAuthority_LegacyOnlyCall",
			call: lipapi.Call{
				Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
			},
			target:     lipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: lipapi.ProjectionReasonConflictingAuthority,
		},
		{
			name: "AssistantPhase",
			call: lipapi.Call{
				Items: []lipapi.Item{{
					Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted,
					Role: lipapi.RoleAssistant, Phase: lipapi.AssistantPhaseCommentary,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "phase text"}},
				}},
			},
			target:     lipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: lipapi.ProjectionReasonAssistantPhase,
		},
		{
			name: "ItemReference",
			call: lipapi.Call{
				Items: []lipapi.Item{{
					Kind: lipapi.ItemKindItemReference, ID: "r1", Status: lipapi.ItemStatusCompleted,
					Reference: &lipapi.ItemReference{ID: "m0"},
				}},
			},
			target:     lipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: lipapi.ProjectionReasonItemReference,
		},
		{
			name: "Compaction",
			call: lipapi.Call{
				Items: []lipapi.Item{{
					Kind: lipapi.ItemKindCompaction, ID: "c1", Status: lipapi.ItemStatusCompleted,
					Compaction: &lipapi.CompactionItem{Dialect: "test"},
				}},
			},
			target:     lipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: lipapi.ProjectionReasonCompaction,
		},
		{
			name: "OpaqueExtension",
			call: lipapi.Call{
				Items: []lipapi.Item{{
					Kind: lipapi.ItemKindExtension, ID: "e1", Status: lipapi.ItemStatusCompleted,
					Extension: &lipapi.OpaqueExtension{Namespace: "custom", Type: "foo"},
				}},
			},
			target:     lipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: lipapi.ProjectionReasonOpaqueExtension,
		},
		{
			name: "VideoInput",
			call: lipapi.Call{
				Items: []lipapi.Item{{
					Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartVideoRef, VideoRef: "vid://1"}},
				}},
			},
			target:     lipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: lipapi.ProjectionReasonVideoInput,
		},
		{
			name: "Annotation",
			call: lipapi.Call{
				Items: []lipapi.Item{{
					Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartAnnotation, Annotation: &lipapi.AnnotationPart{Type: "note"}}},
				}},
			},
			target:     lipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: lipapi.ProjectionReasonAnnotation,
		},
		{
			name: "AssistantMediaRef",
			call: lipapi.Call{
				Items: []lipapi.Item{{
					Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleAssistant,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartAssistantRef, AssistantRef: "ref://1"}},
				}},
			},
			target:     lipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: lipapi.ProjectionReasonAssistantMediaRef,
		},
		{
			name: "Refusal",
			call: lipapi.Call{
				Items: []lipapi.Item{{
					Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleAssistant,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartRefusal, Refusal: "I cannot do that"}},
				}},
			},
			target:     lipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: lipapi.ProjectionReasonRefusal,
		},
		{
			name: "Summary",
			call: lipapi.Call{
				Items: []lipapi.Item{{
					Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleAssistant,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartSummary, Summary: "summary text"}},
				}},
			},
			target:     lipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: lipapi.ProjectionReasonSummary,
		},
		{
			name: "ReasoningReplay_UnsupportedDialect",
			call: lipapi.Call{
				Items: []lipapi.Item{{
					Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleAssistant,
					Content: []lipapi.ContentPart{{
						Kind:      lipapi.ContentPartReasoning,
						Reasoning: &lipapi.ReasoningPart{Dialect: "unknown_dialect", Text: "thinking"},
					}},
				}},
			},
			target:     lipapi.DefaultLegacyProjectionTarget(defaultCaps, noReplay),
			wantReason: lipapi.ProjectionReasonReasoningReplay,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := lipapi.ProjectItemsToLegacyView(tt.call, tt.target)
			if err == nil {
				t.Fatalf("expected projection error for %s", tt.name)
			}
			if !lipapi.IsProjectionError(err) {
				t.Fatalf("expected ProjectionError, got %T: %v", err, err)
			}
			var pe *lipapi.ProjectionError
			if !errors.As(err, &pe) {
				t.Fatalf("errors.As failed for %T", err)
			}
			if pe.Reason != tt.wantReason {
				t.Fatalf("reason mismatch: got %q, want %q (err string: %s)", pe.Reason, tt.wantReason, pe.Error())
			}
		})
	}
}

func TestProjectItemsToLegacyView_developerPlacementAndOrderPreservation(t *testing.T) {
	t.Parallel()

	target := lipapi.DefaultLegacyProjectionTarget(
		lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		lipapi.ReasoningReplaySupport{},
	)

	t.Run("leading_developer_projects_to_instructions", func(t *testing.T) {
		t.Parallel()
		call := lipapi.Call{
			Items: []lipapi.Item{
				{
					Kind:    lipapi.ItemKindMessage,
					ID:      "dev-leading",
					Status:  lipapi.ItemStatusCompleted,
					Role:    lipapi.RoleDeveloper,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "system prompt"}},
				},
				{
					Kind:    lipapi.ItemKindMessage,
					ID:      "user-1",
					Status:  lipapi.ItemStatusCompleted,
					Role:    lipapi.RoleUser,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "user question"}},
				},
			},
		}

		proj, err := lipapi.ProjectItemsToLegacyView(call, target)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(proj.Instructions) != 1 {
			t.Fatalf("expected 1 instruction, got %d", len(proj.Instructions))
		}
		if proj.Instructions[0].Role != lipapi.RoleDeveloper || proj.Instructions[0].Parts[0].Text != "system prompt" {
			t.Fatalf("unexpected instruction: %#v", proj.Instructions[0])
		}
		if len(proj.Messages) != 1 {
			t.Fatalf("expected 1 message, got %d", len(proj.Messages))
		}
		if proj.Messages[0].Role != lipapi.RoleUser || proj.Messages[0].Parts[0].Text != "user question" {
			t.Fatalf("unexpected message: %#v", proj.Messages[0])
		}
	})

	t.Run("mid_trajectory_developer_preserves_position_in_messages", func(t *testing.T) {
		t.Parallel()
		call := lipapi.Call{
			Items: []lipapi.Item{
				{
					Kind:    lipapi.ItemKindMessage,
					ID:      "user-1",
					Status:  lipapi.ItemStatusCompleted,
					Role:    lipapi.RoleUser,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "start turn"}},
				},
				{
					Kind:    lipapi.ItemKindMessage,
					ID:      "dev-recovery",
					Status:  lipapi.ItemStatusCompleted,
					Role:    lipapi.RoleDeveloper,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "<automated-recovery>continue</automated-recovery>"}},
				},
				{
					Kind:    lipapi.ItemKindMessage,
					ID:      "asst-1",
					Status:  lipapi.ItemStatusCompleted,
					Role:    lipapi.RoleAssistant,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "recovered answer"}},
				},
			},
		}

		proj, err := lipapi.ProjectItemsToLegacyView(call, target)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(proj.Instructions) != 0 {
			t.Fatalf("expected 0 instructions, got %d", len(proj.Instructions))
		}
		if len(proj.Messages) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(proj.Messages))
		}
		if proj.Messages[0].Role != lipapi.RoleUser || proj.Messages[0].Parts[0].Text != "start turn" {
			t.Errorf("messages[0] mismatch: %#v", proj.Messages[0])
		}
		if proj.Messages[1].Role != lipapi.RoleDeveloper || proj.Messages[1].Parts[0].Text != "<automated-recovery>continue</automated-recovery>" {
			t.Errorf("messages[1] mid-trajectory developer mismatch: %#v", proj.Messages[1])
		}
		if proj.Messages[2].Role != lipapi.RoleAssistant || proj.Messages[2].Parts[0].Text != "recovered answer" {
			t.Errorf("messages[2] mismatch: %#v", proj.Messages[2])
		}
	})

	t.Run("leading_and_mid_trajectory_mixed_order_preservation", func(t *testing.T) {
		t.Parallel()
		call := lipapi.Call{
			Items: []lipapi.Item{
				{
					Kind:    lipapi.ItemKindMessage,
					ID:      "sys-leading",
					Status:  lipapi.ItemStatusCompleted,
					Role:    lipapi.RoleSystem,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "base system"}},
				},
				{
					Kind:    lipapi.ItemKindMessage,
					ID:      "user-1",
					Status:  lipapi.ItemStatusCompleted,
					Role:    lipapi.RoleUser,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "user query"}},
				},
				{
					Kind:    lipapi.ItemKindMessage,
					ID:      "dev-steering",
					Status:  lipapi.ItemStatusCompleted,
					Role:    lipapi.RoleDeveloper,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "steering injection"}},
				},
				{
					Kind:    lipapi.ItemKindMessage,
					ID:      "user-2",
					Status:  lipapi.ItemStatusCompleted,
					Role:    lipapi.RoleUser,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "user follow-up"}},
				},
			},
		}

		proj, err := lipapi.ProjectItemsToLegacyView(call, target)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(proj.Instructions) != 1 || proj.Instructions[0].Role != lipapi.RoleSystem {
			t.Fatalf("expected 1 leading system instruction, got %#v", proj.Instructions)
		}
		if len(proj.Messages) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(proj.Messages))
		}
		if proj.Messages[0].Role != lipapi.RoleUser || proj.Messages[0].Parts[0].Text != "user query" {
			t.Errorf("messages[0] mismatch: %#v", proj.Messages[0])
		}
		if proj.Messages[1].Role != lipapi.RoleDeveloper || proj.Messages[1].Parts[0].Text != "steering injection" {
			t.Errorf("messages[1] mid-trajectory developer mismatch: %#v", proj.Messages[1])
		}
		if proj.Messages[2].Role != lipapi.RoleUser || proj.Messages[2].Parts[0].Text != "user follow-up" {
			t.Errorf("messages[2] mismatch: %#v", proj.Messages[2])
		}
	})
}
