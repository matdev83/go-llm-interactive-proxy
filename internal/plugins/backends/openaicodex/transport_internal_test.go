package openaicodex

import (
	"errors"
	"strings"
	"testing"
)

func TestWSTransportError_errorsContract(t *testing.T) {
	t.Parallel()
	cause := errors.New("dial boom")
	err := newWSTransportError(cause)
	if err == nil {
		t.Fatal("newWSTransportError(non-nil) = nil, want error")
	}
	var target *wsTransportError
	if !errors.As(err, &target) {
		t.Fatalf("errors.As(*wsTransportError) = false, want true")
	}
	if !errors.Is(target, cause) {
		t.Fatalf("errors.Is(target, cause) = false, want true (Unwrap must expose cause)")
	}
	if msg := err.Error(); !strings.Contains(msg, "websocket transport") || !strings.Contains(msg, "dial boom") {
		t.Fatalf("Error() = %q, want it to contain %q and the cause", msg, "websocket transport")
	}
	if isWSStreamReadError(err) {
		t.Fatalf("transport error must not match wsStreamReadError discriminator")
	}
}

func TestWSTransportError_nilCauseReturnsNil(t *testing.T) {
	t.Parallel()
	if err := newWSTransportError(nil); err != nil {
		t.Fatalf("newWSTransportError(nil) = %v, want nil", err)
	}
}

func TestWSStreamReadError_errorsContract(t *testing.T) {
	t.Parallel()
	cause := errors.New("read boom")
	err := newWSStreamReadError(cause)
	if err == nil {
		t.Fatal("newWSStreamReadError(non-nil) = nil, want error")
	}
	var target *wsStreamReadError
	if !errors.As(err, &target) {
		t.Fatalf("errors.As(*wsStreamReadError) = false, want true")
	}
	if !errors.Is(target, cause) {
		t.Fatalf("errors.Is(target, cause) = false, want true (Unwrap must expose cause)")
	}
	if msg := err.Error(); !strings.Contains(msg, "read websocket") || !strings.Contains(msg, "read boom") {
		t.Fatalf("Error() = %q, want it to contain %q and the cause", msg, "read websocket")
	}
	if isWSTransportFailure(err) {
		t.Fatalf("stream read error must not match wsTransportError discriminator")
	}
}

func TestWSStreamReadError_nilCauseReturnsNil(t *testing.T) {
	t.Parallel()
	if err := newWSStreamReadError(nil); err != nil {
		t.Fatalf("newWSStreamReadError(nil) = %v, want nil", err)
	}
}
