package lipapi_test

import (
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// P2.3 (llm-api-parity): assistant multimodal *response* refs are canonical events only;
// capability negotiation still keys off request shape (RequiredCapabilities), not output events.
func TestRequiredCapabilities_textOnlyUserAndAssistantParts(t *testing.T) {
	t.Parallel()
	c := lipapi.Call{
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("ping")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart("pong")}},
		},
	}
	req := lipapi.RequiredCapabilities(c)
	if slices.Contains(req, lipapi.CapabilityVision) || slices.Contains(req, lipapi.CapabilityDocuments) {
		t.Fatalf("unexpected multimodal caps for text-only messages: %v", req)
	}
}
