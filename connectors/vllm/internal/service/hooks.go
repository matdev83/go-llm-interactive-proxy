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

func resolveFlavor(lipapi.Call) openaicompat.Flavor {
	return openaicompat.FlavorChat
}
