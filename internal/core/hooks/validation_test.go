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
