package lipapi

import "encoding/json"

// ReasoningDialect identifies a provider-neutral replay payload shape for historical reasoning.
// Dialects are adapter-owned vocabulary; canonical contracts store the ID only.
type ReasoningDialect string

// Initial reasoning replay dialect IDs. Adapters own wire meaning; these IDs are stable catalog keys.
const (
	ReasoningDialectOpenAIChatTextV1            ReasoningDialect = "openai.chat.reasoning_text.v1"
	ReasoningDialectOpenAIResponsesItemV1       ReasoningDialect = "openai.responses.reasoning_item.v1"
	ReasoningDialectAnthropicThinkingV1         ReasoningDialect = "anthropic.thinking.v1"
	ReasoningDialectAnthropicRedactedThinkingV1 ReasoningDialect = "anthropic.redacted_thinking.v1"
)

// ReasoningPart is the provider-neutral historical reasoning payload for PartReasoning.
// At least one of Text, Signature, or Opaque must be present when validation is implemented.
type ReasoningPart struct {
	Dialect   ReasoningDialect
	Text      string
	Signature string
	Opaque    json.RawMessage
}

// ReasoningReplaySupport declares which historical reasoning dialects a backend candidate can replay.
type ReasoningReplaySupport struct {
	Dialects []ReasoningDialect
}
