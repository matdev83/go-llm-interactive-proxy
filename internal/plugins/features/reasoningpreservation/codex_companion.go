package reasoningpreservation

import (
	"encoding/json"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// These literals are a private feature-to-Codex trust protocol. The connector
// module intentionally repeats them because it is an independent Go module;
// internal/archtest pins the two copies and the connector removes the marker
// before provider serialization.
const (
	ContinuityMarkerKey   = "lip.internal.openai_codex.reasoning_continuity.v1"
	ContinuityMarkerValue = `{"eligible":true,"dialect":"openai.responses.reasoning_item.v1"}`
)

func deleteClientContinuityMarker(call *lipapi.Call) {
	if call != nil && call.Extensions != nil {
		delete(call.Extensions, ContinuityMarkerKey)
	}
}

func setTrustedContinuityMarker(call *lipapi.Call) {
	if call == nil {
		return
	}
	if call.Extensions == nil {
		call.Extensions = make(map[string]json.RawMessage)
	}
	call.Extensions[ContinuityMarkerKey] = json.RawMessage(ContinuityMarkerValue)
}

func supportsCodexContinuity(call *lipapi.Call, support lipapi.ReasoningReplaySupport) bool {
	if call == nil || !containsDialect(support.Dialects, lipapi.ReasoningDialectOpenAIResponsesItemV1) {
		return false
	}
	for _, msg := range call.Messages {
		for _, part := range msg.Parts {
			if part.Kind != lipapi.PartReasoning || part.Reasoning == nil {
				continue
			}
			if lipapi.NormalizeReasoningDialect(part.Reasoning.Dialect) != lipapi.ReasoningDialectOpenAIResponsesItemV1 {
				return false
			}
		}
	}
	return true
}

func continuityOutcomeSafe(outcomes []SafeOutcome) bool {
	for _, outcome := range outcomes {
		switch outcome {
		case OutcomePreserved, OutcomeRestored, OutcomeMissing, OutcomeUnmatched:
		default:
			return false
		}
	}
	return true
}
