package lipapi_test

import (
	"errors"
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

func TestValidateEventEnvelope_nilEventIsValidationError(t *testing.T) {
	t.Parallel()
	err := lipapi.ValidateEventEnvelope(nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var ve *lipapi.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationError, got %T", err)
	}
	if !errors.Is(err, lipapi.ErrInvalidCall) {
		t.Fatal("expected error to unwrap to ErrInvalidCall")
	}
}
