package lipapi_test

import (
	"math"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestCallReasoningPayloadBytes_aggregatesReasoningOnly(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleAssistant,
			Parts: []lipapi.Part{
				lipapi.TextPart("ignored"),
				{Kind: lipapi.PartReasoning, Reasoning: &lipapi.ReasoningPart{Text: "ab", Signature: "cd"}},
			},
		}},
		Instructions: []lipapi.Message{{
			Role: lipapi.RoleAssistant,
			Parts: []lipapi.Part{
				{Kind: lipapi.PartReasoning, Reasoning: &lipapi.ReasoningPart{Opaque: []byte("ef")}},
			},
		}},
	}
	if got := lipapi.CallReasoningPayloadBytes(call); got != 6 {
		t.Fatalf("got %d want 6", got)
	}
	if got := lipapi.CallReasoningPayloadBytes(nil); got != 0 {
		t.Fatalf("nil got %d", got)
	}
}

func TestSaturatingAddInt64_clamps(t *testing.T) {
	t.Parallel()
	if got := lipapi.SaturatingAddInt64(math.MaxInt64-1, 5); got != math.MaxInt64 {
		t.Fatalf("got %d", got)
	}
}
