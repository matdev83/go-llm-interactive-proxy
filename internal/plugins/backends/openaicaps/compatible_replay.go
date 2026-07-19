package openaicaps

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/reasoningreplay"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	FlavorChat      = "chat"
	FlavorResponses = "responses"
)

func CompatibleReplayEligible(model string, backendPrefixes []string) bool {
	return reasoningreplay.Eligible(model, backendPrefixes)
}

func ForHostedModelCompatibleReplay(model string, backendPrefixes []string) lipapi.BackendCaps {
	caps := ForHostedModel(model)
	if !CompatibleReplayEligible(model, backendPrefixes) {
		return caps
	}
	out := lipapi.NewBackendCaps()
	for c := range caps {
		out[c] = struct{}{}
	}
	out[lipapi.CapabilityReasoningReplay] = struct{}{}
	return out
}

func ResolveCompatibleReplaySupport(flavor, model string, backendPrefixes []string) lipapi.ReasoningReplaySupport {
	if !CompatibleReplayEligible(model, backendPrefixes) {
		return lipapi.ReasoningReplaySupport{}
	}
	switch strings.ToLower(strings.TrimSpace(flavor)) {
	case FlavorResponses:
		return lipapi.ReasoningReplaySupport{
			Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIResponsesItemV1},
		}
	default:
		return lipapi.ReasoningReplaySupport{
			Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIChatTextV1},
		}
	}
}
