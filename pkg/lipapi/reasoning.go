package lipapi

import (
	"encoding/json"
	"slices"
	"strings"
)

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

// NormalizeReasoningDialect returns the canonical dialect form: trim space and lowercase.
// Unknown dialect IDs remain valid when non-empty after normalization and within bounds.
func NormalizeReasoningDialect(d ReasoningDialect) ReasoningDialect {
	return ReasoningDialect(strings.ToLower(strings.TrimSpace(string(d))))
}

// NormalizeReasoningDialects normalizes, omits empty, deduplicates, and sorts dialect IDs.
func NormalizeReasoningDialects(in []ReasoningDialect) []ReasoningDialect {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[ReasoningDialect]struct{}, len(in))
	out := make([]ReasoningDialect, 0, len(in))
	for _, d := range in {
		if n := NormalizeReasoningDialect(d); n != "" {
			if _, ok := seen[n]; !ok {
				seen[n] = struct{}{}
				out = append(out, n)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	slices.Sort(out)
	return out
}

// ReasoningPart is the provider-neutral historical reasoning payload for PartReasoning.
// At least one of Text, Signature, or Opaque must be present.
type ReasoningPart struct {
	Dialect   ReasoningDialect
	Text      string
	Signature string
	Opaque    json.RawMessage
}

// ReasoningPayloadBytes returns Text+Signature+Opaque byte length (dialect excluded).
func ReasoningPayloadBytes(rp *ReasoningPart) int {
	if rp == nil {
		return 0
	}
	return len(rp.Text) + len(rp.Signature) + len(rp.Opaque)
}

// ReasoningReplaySupport declares which historical reasoning dialects a backend candidate can replay.
type ReasoningReplaySupport struct {
	Dialects []ReasoningDialect
}
