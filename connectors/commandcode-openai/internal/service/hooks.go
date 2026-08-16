package service

import (
	"maps"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

const (
	extraBodyPrefixCommandCode = "commandcode.extra_body."
	extraBodyPrefixOpenAI      = "openai.extra_body."
)

// ProviderHooks returns Command Code OpenAI request mutation hooks (tests + production).
func ProviderHooks() openaicompat.RequestHooks {
	return openaicompat.RequestHooks{MutateBody: mutateCommandCode}
}

func resolveModel(kind string, inv backendplugin.Invocation, _ lipapi.Call) string {
	canonical := strings.TrimSpace(inv.CanonicalModelID)
	return strings.TrimPrefix(canonical, kind+"/")
}

func resolveFlavor(call lipapi.Call) openaicompat.Flavor {
	if call.Invocation.Operation == lipapi.OperationOpenAIResponses {
		return openaicompat.FlavorResponses
	}
	return openaicompat.FlavorChat
}

func mutateCommandCode(body map[string]any, call lipapi.Call, _ string, _ openaicompat.Flavor) error {
	for _, prefix := range []string{extraBodyPrefixCommandCode, extraBodyPrefixOpenAI} {
		maps.Copy(body, openaicompat.CollectPrefixedExtraBody(call.Extensions, prefix))
	}
	return nil
}
