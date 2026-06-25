package gemini

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/geminigenerate"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ModelCapabilities returns negotiated backend caps for a resolved Gemini model id.
func ModelCapabilities(model string) lipapi.BackendCaps {
	return geminigenerate.ModelCapabilities(model)
}
