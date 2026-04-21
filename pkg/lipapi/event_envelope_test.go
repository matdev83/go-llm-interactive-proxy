package lipapi_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestValidateEventEnvelope_deltaWithinLimit(t *testing.T) {
	t.Parallel()
	ev := &lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "ok"}
	if err := lipapi.ValidateEventEnvelope(ev); err != nil {
		t.Fatal(err)
	}
}

func TestValidateEventEnvelope_deltaTooLarge(t *testing.T) {
	t.Parallel()
	ev := &lipapi.Event{
		Kind:  lipapi.EventTextDelta,
		Delta: strings.Repeat("x", lipapi.MaxEventDeltaBytes+1),
	}
	if err := lipapi.ValidateEventEnvelope(ev); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateEventEnvelope_assistantImageRequiresRef(t *testing.T) {
	t.Parallel()
	ev := &lipapi.Event{Kind: lipapi.EventAssistantImageRef, AssistantRef: " "}
	if err := lipapi.ValidateEventEnvelope(ev); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateEventEnvelope_errorMessageTooLarge(t *testing.T) {
	t.Parallel()
	ev := &lipapi.Event{
		Kind:         lipapi.EventError,
		ErrorCode:    "x",
		ErrorMessage: strings.Repeat("e", lipapi.MaxEventDiagMessageBytes+1),
	}
	if err := lipapi.ValidateEventEnvelope(ev); err == nil {
		t.Fatal("expected error")
	}
}
