package lipapi_test

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Black-box regression anchors for exported error and helper surfaces (specification bundle).
func TestPublicStreamErrorUnwrapsTerminalRoot(t *testing.T) {
	t.Parallel()
	err := lipapi.NewStreamError("x", "y")
	if !errors.Is(err, lipapi.ErrStreamTerminal) {
		t.Fatalf("NewStreamError must unwrap to ErrStreamTerminal")
	}
	var se *lipapi.StreamError
	if !errors.As(err, &se) || se.Code != "x" || se.Message != "y" {
		t.Fatalf("As StreamError: %#v", err)
	}
	if got := err.Error(); got != lipapi.ErrStreamTerminal.Error() {
		t.Fatalf("StreamError.Error() = %q want stable root text %q", got, lipapi.ErrStreamTerminal.Error())
	}
}

func TestPublicSentinelRootsAreDistinct(t *testing.T) {
	t.Parallel()
	roots := []error{
		lipapi.ErrInvalidCall,
		lipapi.ErrCollectLimitExceeded,
		lipapi.ErrNilEventStream,
		lipapi.ErrNilContext,
		lipapi.ErrNilFixedEventStream,
		lipapi.ErrStreamTerminal,
		lipapi.ErrMaxRouteAttempts,
		lipapi.ErrUnresolvedModelOnlySelector,
		lipapi.ErrAllCandidatesContextLimitExceeded,
		lipapi.ErrCapabilityReject,
		lipapi.ErrHookMutation,
	}
	seen := map[string]struct{}{}
	for _, e := range roots {
		s := e.Error()
		if _, dup := seen[s]; dup {
			t.Fatalf("duplicate sentinel message %q", s)
		}
		seen[s] = struct{}{}
	}
}

func TestPublicNegotiationRejectHelpers(t *testing.T) {
	t.Parallel()
	rej := &lipapi.RejectError{Reason: "nope"}
	if !errors.Is(rej, lipapi.ErrCapabilityReject) {
		t.Fatal("RejectError must unwrap ErrCapabilityReject")
	}
	if !lipapi.IsReject(rej) {
		t.Fatal("IsReject must recognize RejectError")
	}
	if lipapi.IsReject(nil) {
		t.Fatal("IsReject(nil) must be false")
	}
}

func TestPublicHookMutationHelpers(t *testing.T) {
	t.Parallel()
	hm := &lipapi.HookMutationError{Cause: lipapi.ErrHookMutation}
	if !lipapi.IsHookMutation(hm) {
		t.Fatal("IsHookMutation must recognize HookMutationError with ErrHookMutation cause")
	}
	if !lipapi.IsHookMutation(lipapi.ErrHookMutation) {
		t.Fatal("IsHookMutation must recognize ErrHookMutation")
	}
	if lipapi.IsHookMutation(nil) {
		t.Fatal("IsHookMutation(nil) must be false")
	}
}

func TestPublicBackendCapsConstruction(t *testing.T) {
	t.Parallel()
	c := lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools)
	if len(c) != 2 {
		t.Fatalf("expected two capabilities, got %d", len(c))
	}
	if _, ok := c[lipapi.CapabilityStreaming]; !ok {
		t.Fatal("missing streaming")
	}
	if _, ok := c[lipapi.CapabilityTools]; !ok {
		t.Fatal("missing tools")
	}
}
