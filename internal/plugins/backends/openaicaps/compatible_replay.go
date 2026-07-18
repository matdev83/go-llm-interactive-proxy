package openaicaps

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

var compatibleReplayPrefixes = []string{
	"openrouter",
	"openai-legacy",
	"openai-responses",
	"nvidia",
	"huggingface",
	"ollama",
	"ollama-cloud",
	"vllm",
	"lmstudio",
	"llamacpp",
	"opencode-go",
	"opencode-zen",
}

var compatibleReplayModelKeywords = []string{"kimi", "moonshot"}

const (
	FlavorChat      = "chat"
	FlavorResponses = "responses"
)

func CompatibleReplayEligible(model string, backendPrefixes []string) bool {
	return compatibleReplayPrefixMatch(backendPrefixes) && compatibleReplayModelMatch(model)
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

func compatibleReplayModelMatch(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	for _, kw := range compatibleReplayModelKeywords {
		if strings.Contains(m, kw) {
			return true
		}
	}
	return false
}

func compatibleReplayPrefixMatch(prefixes []string) bool {
	if len(prefixes) == 0 {
		return false
	}
	want := make(map[string]struct{}, len(compatibleReplayPrefixes))
	for _, p := range compatibleReplayPrefixes {
		want[p] = struct{}{}
	}
	for _, p := range prefixes {
		p = strings.ToLower(strings.TrimSpace(p))
		if _, ok := want[p]; ok {
			return true
		}
	}
	return false
}
