package service

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

const (
	extraBodyPrefixNVIDIA = "nvidia.extra_body."
	extraBodyPrefixOpenAI = "openai.extra_body."
)

// ProviderHooks returns NVIDIA request mutation hooks (tests + production).
func ProviderHooks() openaicompat.RequestHooks {
	return openaicompat.RequestHooks{MutateBody: mutateNVIDIA}
}

func resolveModel(kind string, inv backendplugin.Invocation, _ lipapi.Call) string {
	return strings.TrimPrefix(strings.TrimSpace(inv.CanonicalModelID), kind+"/")
}

func resolveFlavor(call lipapi.Call) openaicompat.Flavor {
	if call.Invocation.Operation == lipapi.OperationOpenAIResponses {
		return openaicompat.FlavorResponses
	}
	return openaicompat.FlavorChat
}

func mutateNVIDIA(body map[string]any, call lipapi.Call, _ string, _ openaicompat.Flavor) error {
	delete(body, "stream_options")
	if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens > 0 {
		body["max_tokens"] = *call.Options.MaxOutputTokens
		delete(body, "max_completion_tokens")
	}
	for _, prefix := range []string{extraBodyPrefixNVIDIA, extraBodyPrefixOpenAI} {
		for field, v := range openaicompat.CollectPrefixedExtraBody(call.Extensions, prefix) {
			body[field] = v
		}
	}
	return nil
}
