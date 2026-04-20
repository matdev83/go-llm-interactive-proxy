package capabilities_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestDefaultNegotiator_lossless(t *testing.T) {
	t.Parallel()

	var n capabilities.DefaultNegotiator
	res := n.Negotiate(context.Background(),
		[]lipapi.Capability{lipapi.CapabilityTools},
		lipapi.NewBackendCaps(lipapi.CapabilityTools, lipapi.CapabilityStreaming),
	)
	if res.Kind != lipapi.NegotiationLossless {
		t.Fatalf("got %s", res.Kind)
	}
}

func TestRequire_negotiatesCall(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "t"}},
	}

	res := capabilities.Require(context.Background(), call,
		lipapi.NewBackendCaps(lipapi.CapabilityTools), nil)
	if res.Kind != lipapi.NegotiationLossless {
		t.Fatalf("got %s", res.Kind)
	}
}

func TestRequire_rejectMissing(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "t"}},
	}

	res := capabilities.Require(context.Background(), call,
		lipapi.NewBackendCaps(lipapi.CapabilityStreaming), nil)
	if res.Kind != lipapi.NegotiationReject {
		t.Fatalf("got %s", res.Kind)
	}
}
