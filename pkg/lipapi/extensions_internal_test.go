package lipapi_test

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestDeriveProtocolRequirements_ignoresFrontendWireMetadataExtensions(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"openairesponses.model":      json.RawMessage(`"gpt-4o-mini"`),
			"openrouter.upstream_flavor": json.RawMessage(`"responses"`),
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	req := lipapi.DeriveProtocolRequirements(call)
	for _, c := range req.Capabilities {
		if c == lipapi.CapabilityOpaqueExtensions {
			t.Fatalf("wire metadata must not require opaque extensions: %#v", req)
		}
	}
	if len(req.ExtensionTypes) != 0 {
		t.Fatalf("extension types=%#v", req.ExtensionTypes)
	}
}
