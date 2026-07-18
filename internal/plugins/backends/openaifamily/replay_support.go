package openaifamily

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicaps"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func ResolveReplaySupport(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.ReasoningReplaySupport {
	model := strings.TrimSpace(cand.Primary.Model)
	prefixes := []string{strings.TrimSpace(cand.Primary.Backend)}
	flavor := openaicaps.FlavorChat
	if ResolveFlavor(call) == openaicompat.FlavorResponses {
		flavor = openaicaps.FlavorResponses
	}
	return openaicaps.ResolveCompatibleReplaySupport(flavor, model, prefixes)
}

func ChatReplaySupport() lipapi.ReasoningReplaySupport {
	return lipapi.ReasoningReplaySupport{}
}

func ResponsesReplaySupport() lipapi.ReasoningReplaySupport {
	return lipapi.ReasoningReplaySupport{}
}
