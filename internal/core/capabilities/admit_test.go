package capabilities_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestAdmitCandidate_acceptsNativeOrderedCompaction(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Items: []lipapi.Item{
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
	res := capabilities.AdmitCandidate(context.Background(), call, lipapi.Invocation{}, routing.AttemptCandidate{}, capabilities.CandidateFacts{
		Caps: lipapi.NewBackendCaps(
			lipapi.CapabilityStreaming,
			lipapi.CapabilityOrderedItems,
			lipapi.CapabilityCompaction,
		),
		DialectSupport: lipapi.DialectSupport{
			CompactionDialects: []lipapi.DialectRequirement{{
				Kind: "compaction", Dialect: "compact.v1",
			}},
			ItemDialects: []lipapi.DialectRequirement{{
				Kind: "item", Dialect: "compact.v1",
			}},
		},
		ProjectionTarget: lipapi.DefaultLegacyProjectionTarget(
			lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityOrderedItems, lipapi.CapabilityCompaction),
			lipapi.ReasoningReplaySupport{},
		),
	})
	if res.Kind != lipapi.NegotiationLossless {
		t.Fatalf("got kind=%s err=%v", res.Kind, res.Err())
	}
}

func TestAdmitCandidate_acceptsReasoningDowngrade(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
		Options:  lipapi.GenerationOptions{ReasoningEffort: "high"},
	}
	res := capabilities.AdmitCandidate(context.Background(), call, lipapi.Invocation{}, routing.AttemptCandidate{}, capabilities.CandidateFacts{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		ProjectionTarget: lipapi.LegacyProjectionTargetFromCaps(
			lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			lipapi.ReasoningReplaySupport{},
		),
	})
	if res.Kind != lipapi.NegotiationDowngrade {
		t.Fatalf("got kind=%s err=%v", res.Kind, res.Err())
	}
}

func TestFailoverRequirementSet_retainsCompleteRequirements(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
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
		},
	}
	set := capabilities.NewFailoverRequirementSet(call)
	supported := lipapi.ProtocolRequirements{
		Capabilities: []lipapi.Capability{lipapi.CapabilityOrderedItems, lipapi.CapabilityAssistantPhase},
	}
	if !set.CandidateMatchesFailoverRequirements(supported, lipapi.ReasoningReplaySupport{}) {
		t.Fatal("expected matching candidate")
	}
	supported.Capabilities = []lipapi.Capability{lipapi.CapabilityOrderedItems}
	if set.CandidateMatchesFailoverRequirements(supported, lipapi.ReasoningReplaySupport{}) {
		t.Fatal("expected failover mismatch without assistant phase")
	}
}
