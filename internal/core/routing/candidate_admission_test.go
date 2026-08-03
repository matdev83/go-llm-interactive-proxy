package routing_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestCandidateAdmissionCheck_rejectsMissingCapability(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "fn"}},
	}
	res := routing.CandidateAdmissionCheck{
		Call:        call,
		BackendCaps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
	}.Evaluate()
	if res.Kind != lipapi.NegotiationReject {
		t.Fatalf("got %s", res.Kind)
	}
}

func TestCandidateAdmissionCheck_acceptsPortableLegacyProjection(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	res := routing.CandidateAdmissionCheck{
		Call:              call,
		BackendCaps:       lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		RequireProjection: true,
		ProjectionTarget:  lipapi.DefaultLegacyProjectionTarget(lipapi.NewBackendCaps(lipapi.CapabilityStreaming), lipapi.ReasoningReplaySupport{}),
	}.Evaluate()
	if res.Kind != lipapi.NegotiationLossless {
		t.Fatalf("got %s err=%v", res.Kind, res.Err())
	}
}
