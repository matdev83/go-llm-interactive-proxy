package lipapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestAdmitCandidate_transportPrecedesCapability(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIChatCompletions,
			DeliveryMode: lipapi.DeliveryModeStreaming,
		},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	res := lipapi.AdmitCandidate(lipapi.CandidateAdmissionInput{
		Call:       call,
		Invocation: call.Invocation,
		BackendCaps: lipapi.NewBackendCaps(
			lipapi.CapabilityStreaming,
			lipapi.CapabilityTools,
		),
		TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIChatCompletions,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeNonStreaming},
		}),
		TransportPolicy: lipapi.TransportFallbackExact,
	})
	if res.Kind != lipapi.NegotiationReject || res.Transport.Kind != lipapi.NegotiationReject {
		t.Fatalf("got %+v", res)
	}
}

func TestProjectLegacyToOrderedItems_toolResultValidates(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{
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
	items, _, err := lipapi.ProjectLegacyToOrderedItems(call, lipapi.DefaultOrderedItemProjectionTarget())
	if err != nil {
		t.Fatalf("ProjectLegacyToOrderedItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items=%#v", items)
	}
	var toolResult *lipapi.ToolResultItem
	for i := range items {
		if items[i].Kind == lipapi.ItemKindToolResult {
			toolResult = items[i].ToolResult
			break
		}
	}
	if toolResult == nil {
		t.Fatal("missing tool result item")
	}
	if err := (lipapi.Call{Items: items}).Validate(); err != nil {
		t.Fatalf("validate: %v items=%#v", err, items)
	}
	if toolResult.Output != "72F" || len(toolResult.Parts) > 0 {
		t.Fatalf("tool result=%#v", toolResult)
	}
}

func TestProjectItemsToLegacyView_rejectsStructuredToolOutput(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Items: []lipapi.Item{
			{
				Kind:   lipapi.ItemKindToolCall,
				ID:     "tc-1",
				Status: lipapi.ItemStatusCompleted,
				ToolCall: &lipapi.ToolCallItem{
					CallID: "c1", Name: "fn", Arguments: json.RawMessage(`{}`),
				},
			},
			{
				Kind:   lipapi.ItemKindToolResult,
				ID:     "tr-1",
				Status: lipapi.ItemStatusCompleted,
				ToolResult: &lipapi.ToolResultItem{
					CallID: "c1",
					Name:   "fn",
					Parts:  []lipapi.ContentPart{{Kind: lipapi.ContentPartImageRef, ImageRef: "img://x"}},
				},
			},
		},
	}
	target := lipapi.LegacyProjectionTargetFromCaps(
		lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
		lipapi.ReasoningReplaySupport{},
	)
	_, err := lipapi.ProjectItemsToLegacyView(call, target)
	if !lipapi.IsProjectionError(err) {
		t.Fatalf("expected projection error, got %v", err)
	}
}

func TestDeriveProtocolRequirements_preservesCallExtensionKeys(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"beta":  json.RawMessage(`{"k":1}`),
			"alpha": json.RawMessage(`{"k":2}`),
		},
	}
	req := lipapi.DeriveProtocolRequirements(call)
	if len(req.ExtensionTypes) != 2 {
		t.Fatalf("extensions=%#v", req.ExtensionTypes)
	}
	if req.ExtensionTypes[0].Type != "alpha" || req.ExtensionTypes[1].Type != "beta" {
		t.Fatalf("extension order/types=%#v", req.ExtensionTypes)
	}
}

func TestExecutor_projectionRejectBeforeBackendOpen_NoNetwork(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"legacy": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityOrderedItems),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return nil, errors.New("should not open")
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "legacy:g"},
		Items: []lipapi.Item{{
			Kind:   lipapi.ItemKindCompaction,
			ID:     "cmp-1",
			Status: lipapi.ItemStatusCompleted,
			Compaction: &lipapi.CompactionItem{
				Dialect: "compact.v1",
				Opaque:  json.RawMessage(`{"ok":true}`),
			},
		}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&opens) != 0 {
		t.Fatalf("backend opened despite projection reject, opens=%d", opens)
	}
}

func TestExecutor_failoverRetainsRequirementSet_NoNetwork(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"partial": {
			Caps: lipapi.NewBackendCaps(
				lipapi.CapabilityStreaming,
				lipapi.CapabilityOrderedItems,
				lipapi.CapabilityAssistantPhase,
			),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return nil, errors.New("should not open")
			},
		},
		"full": {
			Caps: lipapi.NewBackendCaps(
				lipapi.CapabilityStreaming,
				lipapi.CapabilityOrderedItems,
				lipapi.CapabilityAssistantPhase,
				lipapi.CapabilityCompaction,
			),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return nil, errors.New("should not open")
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "partial:g,full:g"},
		Items: []lipapi.Item{
			{
				Kind:   lipapi.ItemKindMessage,
				ID:     "msg-1",
				Status: lipapi.ItemStatusCompleted,
				Role:   lipapi.RoleAssistant,
				Phase:  lipapi.AssistantPhaseFinalAnswer,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "answer"},
				},
			},
			{
				Kind:   lipapi.ItemKindCompaction,
				ID:     "cmp-1",
				Status: lipapi.ItemStatusCompleted,
				Compaction: &lipapi.CompactionItem{
					Dialect: "compact.v1",
					Opaque:  json.RawMessage(`{"ok":true}`),
				},
			},
		},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&opens) != 0 {
		t.Fatalf("expected zero backend opens during admission/failover, opens=%d", opens)
	}
}

func TestProjectItemsToLegacyView_rejectsRefusalAndSummary(t *testing.T) {
	t.Parallel()

	target := lipapi.LegacyProjectionTargetFromCaps(
		lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityStructuredOutputs),
		lipapi.ReasoningReplaySupport{},
	)
	for name, call := range map[string]lipapi.Call{
		"refusal": {Items: []lipapi.Item{{
			Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleAssistant,
			Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartRefusal, Refusal: "no"}},
		}}},
		"summary": {Items: []lipapi.Item{{
			Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleAssistant,
			Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartSummary, Summary: "tl;dr"}},
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := lipapi.ProjectItemsToLegacyView(call, target)
			if !lipapi.IsProjectionError(err) {
				t.Fatalf("expected projection error, got %v", err)
			}
		})
	}
}

func TestExecutor_projectionOnlyRejectAfterDialectMatch_NoNetwork(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Items: []lipapi.Item{{
			Kind: lipapi.ItemKindItemReference, ID: "ref-1", Status: lipapi.ItemStatusCompleted,
			Reference: &lipapi.ItemReference{ID: "msg-prev"},
		}},
	}
	res := capabilities.AdmitCandidate(context.Background(), call, lipapi.Invocation{}, routing.AttemptCandidate{}, capabilities.CandidateFacts{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		ProjectionTarget: lipapi.LegacyProjectionTargetFromCaps(
			lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			lipapi.ReasoningReplaySupport{},
		),
	})
	if res.Kind != lipapi.NegotiationReject {
		t.Fatalf("got kind=%s err=%v", res.Kind, res.Err())
	}
}
