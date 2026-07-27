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
	canonical := strings.TrimSpace(inv.CanonicalModelID)
	model := strings.TrimPrefix(canonical, kind+"/")
	return ApplyProviderSuffix(model, backendplugin.RouteParam(inv.SafeMetadata, "provider"))
}

func resolveFlavor(call lipapi.Call) openaicompat.Flavor {
	if call.Invocation.Operation == lipapi.OperationOpenAIResponses {
		return openaicompat.FlavorResponses
	}
	return openaicompat.FlavorChat
}

// ApplyProviderSuffix appends :provider from the route selector query param.
func ApplyProviderSuffix(model, provider string) string {
	if model == "" {
		return model
	}
	slug := strings.TrimSpace(provider)
	if slug == "" {
		return model
	}
	last := model
	if i := strings.LastIndex(model, "/"); i >= 0 {
		last = model[i+1:]
	}
	if strings.Contains(last, ":") {
		return model
	}
	return model + ":" + slug
}
