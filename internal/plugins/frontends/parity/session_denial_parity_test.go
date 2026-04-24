package parity_test

import (
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/execerr"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// TestSessionDenial_semanticParity_invalidVsOwner asserts invalid authority and owner mismatch share
// the same client-visible message (non-enumerating) per lipapi + lipapidenial mapping.
func TestSessionDenial_semanticParity_invalidVsOwner(t *testing.T) {
	t.Parallel()
	errs := []error{
		lipapi.NewSessionDenialInvalidAuthority("a"),
		lipapi.NewSessionDenialOwnerMismatch("b"),
	}
	var firstMsg string
	for i, err := range errs {
		out := execerr.ClassifyExecute(err)
		if out.Status != http.StatusBadRequest {
			t.Fatalf("case %d: status %d", i, out.Status)
		}
		if i == 0 {
			firstMsg = out.Message
			continue
		}
		if out.Message != firstMsg {
			t.Fatalf("case %d: message %q differs from first %q (non-enumerating)", i, out.Message, firstMsg)
		}
	}
}

func TestSessionDenial_semanticParity_missingPrincipal401(t *testing.T) {
	t.Parallel()
	out := execerr.ClassifyExecute(lipapi.NewSessionDenialMissingPrincipal("x"))
	if out.Status != http.StatusUnauthorized {
		t.Fatalf("status: %d", out.Status)
	}
	if execerr.OpenAIWireErrorType(out.Status) != "authentication_error" {
		t.Fatalf("openai type: %s", execerr.OpenAIWireErrorType(out.Status))
	}
}

func TestSessionDenial_semanticParity_storage503(t *testing.T) {
	t.Parallel()
	out := execerr.ClassifyExecute(lipapi.NewSessionDenialStorageUnavailable("x"))
	if out.Status != http.StatusServiceUnavailable {
		t.Fatalf("status: %d", out.Status)
	}
}
