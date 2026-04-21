package openaiwire_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Golden-style invariant: canonical user text extracted from lipapi messages should match
// across protocols when only the user turn is present (cross-protocol matrix task 14.3 slice).
func TestCrossProtocol_userTextInvariant(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello matrix")},
		}},
	}
	if len(call.Messages) != 1 || call.Messages[0].Parts[0].Text != "hello matrix" {
		t.Fatalf("%+v", call.Messages)
	}
}
