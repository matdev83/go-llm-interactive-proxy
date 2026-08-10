package reasoningpreservation_test

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

func exactResponsesSupport() lipapi.ReasoningReplaySupport {
	return lipapi.ReasoningReplaySupport{Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIResponsesItemV1}}
}
