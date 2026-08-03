package lipapi_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestAdmitCandidate_unionsFrozenBaselineWithCurrentCall_NoNetwork(t *testing.T) {
	t.Parallel()

	frozen := lipapi.ProtocolRequirements{
		Capabilities: []lipapi.Capability{lipapi.CapabilityStreaming},
	}
	call := lipapi.Call{
		Tools: []lipapi.ToolDef{{Name: "added-by-hook"}},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIChatCompletions,
			DeliveryMode: lipapi.DeliveryModeNonStreaming,
		},
	}
	res := lipapi.AdmitCandidate(lipapi.CandidateAdmissionInput{
		Call:        call,
		Invocation:  call.Invocation,
		BackendCaps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIChatCompletions,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeNonStreaming},
		}),
		FrozenRequirements: &frozen,
	})
	if res.Kind != lipapi.NegotiationReject {
		t.Fatalf("expected tools reject without backend capability, got %+v", res)
	}
}

func TestAdmitCandidate_acceptsPortableItemAuthorityLegacyProjection(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Items: []lipapi.Item{
			{
				Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser,
				Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "hello"}},
			},
			{
				Kind: lipapi.ItemKindToolCall, ID: "tc1", Status: lipapi.ItemStatusCompleted,
				ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "weather", Arguments: []byte(`{}`)},
			},
		},
	}
	res := lipapi.AdmitCandidate(lipapi.CandidateAdmissionInput{
		Call: call,
		BackendCaps: lipapi.NewBackendCaps(
			lipapi.CapabilityStreaming,
			lipapi.CapabilityTools,
		),
		ProjectionTarget: lipapi.LegacyProjectionTargetFromCaps(
			lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
			lipapi.ReasoningReplaySupport{},
		),
	})
	if res.Kind != lipapi.NegotiationLossless {
		t.Fatalf("got kind=%s err=%v cap=%+v", res.Kind, res.Err(), res.Capability)
	}
}
