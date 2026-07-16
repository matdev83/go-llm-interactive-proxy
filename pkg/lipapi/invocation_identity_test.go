package lipapi_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestInvocation_ClientUserAgentNotSerializedOnCall(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "c1",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Invocation: lipapi.Invocation{
			Operation:       lipapi.OperationOpenAIResponses,
			DeliveryMode:    lipapi.DeliveryModeStreaming,
			ClientUserAgent: "SecretAgent/9.9",
		},
	}
	raw, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, "SecretAgent") {
		t.Fatalf("ClientUserAgent leaked into JSON: %s", s)
	}
	if strings.Contains(s, "ClientUserAgent") || strings.Contains(s, "user_agent") || strings.Contains(s, "User-Agent") {
		t.Fatalf("identity metadata keys leaked into JSON: %s", s)
	}
}

func TestInvocation_ClientUserAgentNotSerializedDirectly(t *testing.T) {
	t.Parallel()
	inv := lipapi.Invocation{
		Operation:       lipapi.OperationOpenAIChatCompletions,
		ClientUserAgent: "DirectAgent/1.0",
	}
	raw, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "DirectAgent") {
		t.Fatalf("ClientUserAgent leaked from Invocation JSON: %s", raw)
	}
}
