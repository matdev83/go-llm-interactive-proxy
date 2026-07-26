package service

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func providerHooks() openaicompat.RequestHooks {
	return openaicompat.RequestHooks{}
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
