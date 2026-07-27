package acp

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ToolUpdateSink receives coalesced ACP tool_call / tool_call_update payloads when the
// canonical lipapi tool stream is not yet wired (conformance matrix). Nil means the
// mapper emits warnings or drops tool updates per SessionUpdateMapperOptions.
type ToolUpdateSink interface {
	HandleToolUpdate(ctx context.Context, kind string, update map[string]any) ([]lipapi.Event, error)
}
