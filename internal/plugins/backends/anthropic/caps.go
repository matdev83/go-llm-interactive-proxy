package anthropic

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/anthropicmessages"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ModelCapabilities returns negotiated backend caps for a resolved Anthropic model id.
// Rules are intentionally small; expand as catalog data grows.
func ModelCapabilities(model string) lipapi.BackendCaps {
	return anthropicmessages.ModelCapabilities(model)
}
