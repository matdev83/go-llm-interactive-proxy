package anthropicmessages

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

// ReplaySupport is the static historical-reasoning dialect profile for Anthropic Messages.
func ReplaySupport() lipapi.ReasoningReplaySupport {
	return lipapi.ReasoningReplaySupport{
		Dialects: []lipapi.ReasoningDialect{
			lipapi.ReasoningDialectAnthropicThinkingV1,
			lipapi.ReasoningDialectAnthropicRedactedThinkingV1,
		},
	}
}
