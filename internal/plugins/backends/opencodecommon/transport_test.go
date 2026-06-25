package opencodecommon

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestTransportCaps_exposeOpenAIOperationsForMultiFlavorDispatch(t *testing.T) {
	t.Parallel()

	caps := TransportCaps()
	if !caps.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeNonStreaming) {
		t.Fatal("chat non-streaming must be advertised")
	}
	if !caps.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeStreaming) {
		t.Fatal("responses streaming must be advertised")
	}
	if caps.DeclaredFor(lipapi.Operation("anthropic.messages")) {
		t.Fatal("anthropic operation is not a core transport surface; dispatch is internal")
	}
}
