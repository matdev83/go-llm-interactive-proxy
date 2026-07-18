package hooks_test

import (
	"strings"
	"testing"

	corehooks "github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestValidateEventAfterResponseHook_rejectsOversizedDelta(t *testing.T) {
	t.Parallel()
	ev := &lipapi.Event{
		Kind:  lipapi.EventTextDelta,
		Delta: strings.Repeat("x", lipapi.MaxEventDeltaBytes+1),
	}
	err := corehooks.ValidateEventAfterResponseHook("test-hook", ev)
	if err == nil {
		t.Fatal("expected HookMutationError for oversized delta")
	}
}

func TestValidateEventAfterResponseHook_acceptsReasoningSignatureDelta(t *testing.T) {
	t.Parallel()
	ev := &lipapi.Event{
		Kind:      lipapi.EventReasoningSignatureDelta,
		Signature: "sig-plan",
	}
	if err := corehooks.ValidateEventAfterResponseHook("test-hook", ev); err != nil {
		t.Fatalf("RED: SignatureDelta must be a known post-hook event kind: %v", err)
	}
}

func TestValidateEventAfterResponseHook_acceptsReasoningOpaqueDelta(t *testing.T) {
	t.Parallel()
	ev := &lipapi.Event{
		Kind:   lipapi.EventReasoningOpaqueDelta,
		Opaque: []byte(`{"type":"redacted_thinking","data":"x"}`),
	}
	if err := corehooks.ValidateEventAfterResponseHook("test-hook", ev); err != nil {
		t.Fatalf("RED: OpaqueDelta must be a known post-hook event kind: %v", err)
	}
}
